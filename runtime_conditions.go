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

package spx

import (
	coreevent "github.com/goplus/spx/v3/internal/core/event"
	"github.com/goplus/spx/v3/internal/coroutine"
)

// OnCond handles rising edges without reentry, skipping polls while active.
// Conditions must be fast and non-blocking.
func (p *scriptEventBindings) OnCond(__xgo_autoclosure_condition func() bool, onCondition func()) {
	if __xgo_autoclosure_condition == nil || onCondition == nil {
		return
	}
	running := false
	edge := coreevent.MatchRisingEdge(__xgo_autoclosure_condition)
	p.scriptEventRegistry.manager.AddCondition(coreevent.NewSink(
		p.pthis,
		func() {
			running = true
			defer func() { running = false }()
			onCondition()
		},
		func(data any) bool { return !running && edge(data) },
	))
}

// sampleConditions 在帧时钟推进前，对全部 OnCond 条件进行一次一致性采样。
//
// 条件读取可能访问脚本共享状态，因此存在协程管理器时通过 ReadScriptState
// 暂停脚本写入。这里只把本次上升沿匹配的 sink 保存到 pendingConditions，
// 不执行 handler，也不创建对应 Thread。
func (p *scriptEventRegistry) sampleConditions() {
	read := func() {
		// 取出全部条件 sink，按 Scratch 目标顺序排列，并逐个执行 Cond 判断。
		p.pendingConditions = matchingEventSinks(p.globalSinks(coreevent.BucketCondition), nil)
	}
	if gco == nil {
		read()
	} else {
		gco.ReadScriptState(read)
	}
}

// dispatchConditions 异步启动上一次 sampleConditions 已经匹配的 OnCond handler。
//
// 它先取走并清空 pendingConditions，再直接调用 dispatchMatchedScriptEventBatch；
// 因此不会在帧时钟推进后重复求值条件，handler 观察到的是采样阶段确定的触发结果。
func (p *scriptEventRegistry) dispatchConditions() {
	sinks := p.pendingConditions
	p.pendingConditions = nil
	if len(sinks) == 0 {
		return
	}
	runScriptEventDispatch(func() {
		dispatchMatchedScriptEventBatch(sinks, scriptEventDispatch{
			mode: coroutine.BatchAsync,
			run: func(_ coroutine.Thread, sink *eventSink) {
				sink.Handler.(func())()
			},
		})
	})
}
