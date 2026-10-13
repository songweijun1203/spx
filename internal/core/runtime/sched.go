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
	MainStartedAt   time.Time
	Now             time.Time
	MainExecTimeout time.Duration
}

type SchedulerHooks struct {
	SchedCurrent   func()
	IsSchedTimeout func(float64) bool
	OnSchedTimeout func()
}

func MainExecutionTimedOut(state ScheduleState) bool {
	return !state.MainStartedAt.IsZero() && state.Now.Sub(state.MainStartedAt) >= state.MainExecTimeout
}

func SchedNow(state ScheduleState, hooks SchedulerHooks) error {
	if MainExecutionTimedOut(state) {
		return ErrMainExecutionTimedOut
	}
	if hooks.SchedCurrent != nil {
		hooks.SchedCurrent()
	}
	return nil
}

func Sched(state ScheduleState, schedTimeoutMs float64, hooks SchedulerHooks) error {
	if MainExecutionTimedOut(state) {
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

// Forever 只负责循环结构：每次 call 返回后都调用一次由上层注入的 yield。
// yield 并不一定表示“等待下一帧”。SPX 正常运行时传入 NewControlFlowWaiter：
// 它先把当前协程登记为循环续体并让出，是否在同一引擎帧恢复由协程调度器根据
// 重绘标记和同帧工作预算统一决定。
func Forever(call func(), yield func()) {
	if call == nil {
		return
	}
	for {
		// 一轮用户逻辑在同一个脚本执行片段中完整运行；调度切换发生在下面的
		// yield，其他 SPX 脚本不会在普通语句之间插入执行。
		call()
		// 普通模式最终进入 Coroutines.YieldLoopFor；恢复后才开始下一轮 call。
		yield()
	}
}

func Repeat(loopCount int, call func(), yield func()) {
	if call == nil {
		return
	}
	for range loopCount {
		call()
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
