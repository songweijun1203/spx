/*
 * Copyright (c) 2021 The XGo Authors (xgo.dev). All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package runtime

import (
	"errors"
	"time"
)

const (
	MainExecutionTimedOutMsg = "Main execution timed out. Please check if there is an infinite loop in the code."
	LoopExecutionTimedOutMsg = "For loop execution timed out. Please check if there is an infinite loop in the code."
)

//lint:ignore ST1005 This wraps a user-facing runtime message kept as a complete sentence.
var ErrMainExecutionTimedOut = errors.New(MainExecutionTimedOutMsg)

//lint:ignore ST1005 This wraps a user-facing runtime message kept as a complete sentence.
var ErrLoopExecutionTimedOut = errors.New(LoopExecutionTimedOutMsg)

type ScheduleState struct {
	IsSchedInMain   bool
	MainSchedTime   time.Time
	Now             time.Time
	MainExecTimeout time.Duration
}

type SchedulerHooks struct {
	SchedCurrent   func()
	IsSchedTimeout func(float64) bool
	OnSchedTimeout func()
}

func MainSchedTimedOut(state ScheduleState) bool {
	if !state.IsSchedInMain || state.MainSchedTime.IsZero() {
		return false
	}
	return state.Now.Sub(state.MainSchedTime) >= state.MainExecTimeout
}

func SchedNow(state ScheduleState, hooks SchedulerHooks) error {
	if MainSchedTimedOut(state) {
		return ErrMainExecutionTimedOut
	}
	if hooks.SchedCurrent != nil {
		hooks.SchedCurrent()
	}
	return nil
}

func Sched(state ScheduleState, schedTimeoutMs float64, hooks SchedulerHooks) error {
	if MainSchedTimedOut(state) {
		return ErrMainExecutionTimedOut
	}
	if hooks.IsSchedTimeout != nil && hooks.IsSchedTimeout(schedTimeoutMs) {
		if hooks.OnSchedTimeout != nil {
			hooks.OnSchedTimeout()
		}
		return ErrLoopExecutionTimedOut
	}
	return nil
}

func RunMain(call func(), now time.Time, setSchedInMain func(bool), setMainSchedTime func(time.Time)) {
	setSchedInMain(true)
	setMainSchedTime(now)
	defer setSchedInMain(false)
	call()
}

func Forever(call func(), yield func()) {
	if call == nil {
		return
	}
	// Forever 本身只实现“执行一次循环体，再调用一次协作式 yield”的结构。
	// 它不会创建新的 Thread，也不会自己计算帧时间；具体 yield 行为由调用方
	// 注入。SPX 传入的是 engine.NewControlFlowWaiter：普通模式调用 YieldLoopFor；
	// Warp 模式在独立预算未到时直接返回，预算到期后才调用 WaitNextFrameFor。
	for {
		// call 返回后才到达循环边界。若循环体内部执行了可视状态修改，相关 API
		// 会调用 RequestRedraw，调度器会在本轮结束后禁止同帧下一轮。
		call()
		// yield 让出当前 Thread。普通模式创建 waitTypeLoop Job；没有重绘且
		// beginUpdate 建立的共享 workDeadline 尚未到期时，该 Job 可能在本物理帧
		// 的下一脚本轮次恢复。
		// yield 正常返回，表示同一个 Thread 已经被调度器重新 Resume 并重新取得
		// runMu。Go for 才从这里回到循环顶部，再执行下一次 call；因此同帧多轮
		// 本质上是同一个调用栈在 Yield 内睡眠后继续，不是递归，也不是创建新协程。
		yield()
	}
}

func Repeat(loopCount int, call func(), yield func()) {
	if call == nil {
		return
	}
	for range loopCount {
		call()
		// Repeat 的每轮边界与 Forever 相同：是否同帧继续由注入的 yield 和
		// 协程调度器决定，而不是 Go for 循环直接连续执行到底。
		yield()
	}
}

func RepeatUntil(condition func() bool, call func(), yield func()) {
	if call == nil || condition == nil {
		return
	}
	for {
		if condition() {
			return
		}
		call()
		yield()
	}
}

func WaitUntil(condition func() bool, yield func()) {
	if condition == nil {
		return
	}
	for {
		if condition() {
			return
		}
		yield()
	}
}
