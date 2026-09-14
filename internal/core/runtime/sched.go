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

func Forever(call func(), waitNextFrame func()) {
	if call == nil {
		return
	}
	// 这是 `.spx` forever 在运行时对应的实际无限循环。包含 forever 的
	// OnStart/OnMsg/OnKey 等事件只负责启动一次脚本协程；后续每一轮不是新事件，
	// 也不经过 eventLoop，而是在同一个协程恢复后继续执行。
	for {
		call()
		// 名字保留了旧语义，当前传入的是控制流 waiter：普通模式在循环边界
		// 产生 waitTypeLoop 并让出；调度器再决定同一引擎帧开启下一轮，还是
		// 延迟到下一帧。若循环体内部执行 wait，则会先在 wait 处让出。
		waitNextFrame()
	}
}

func Repeat(loopCount int, call func(), waitNextFrame func()) {
	if call == nil {
		return
	}
	for range loopCount {
		call()
		waitNextFrame()
	}
}

func RepeatUntil(condition func() bool, call func(), waitNextFrame func()) {
	if call == nil || condition == nil {
		return
	}
	for {
		if condition() {
			return
		}
		call()
		waitNextFrame()
	}
}

func WaitUntil(condition func() bool, waitNextFrame func()) {
	if condition == nil {
		return
	}
	for {
		if condition() {
			return
		}
		waitNextFrame()
	}
}
