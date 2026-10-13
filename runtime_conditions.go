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

// OnCond 注册条件从 false 变为 true 时执行的处理器。
// 同一个处理器仍在运行时跳过条件求值，避免重入；条件函数在帧更新前的采样阶段
// 同步执行，因此必须快速完成且不能阻塞。
func (p *scriptEventBindings) OnCond(__xgo_autoclosure_condition func() bool, onCondition func()) {
	if __xgo_autoclosure_condition == nil || onCondition == nil {
		return
	}
	running := false
	edge := coreevent.MatchRisingEdge(__xgo_autoclosure_condition)
	p.scriptEventRegistry.manager.Add(coreevent.BucketCondition, coreevent.NewSink(
		p.owner,
		func() {
			running = true
			defer func() { running = false }()
			onCondition()
		},
		// running 使用短路求值：处理器尚未结束时连条件函数也不会调用。
		func(data any) bool { return !running && edge(data) },
	))
}

// sampleConditions 在逻辑时钟推进前评估全部 OnCond，并保存本帧命中的处理器快照。
// 它只执行条件函数，不启动事件处理协程。RunBetweenScripts 取得调度器的 runMu，
// 保证采样不会与某个用户脚本切片并发，从而让所有条件观察一致的游戏状态。
func (p *scriptEventRegistry) sampleConditions() {
	read := func() {
		// matchingEventSinks 会调用每个 sink 的上升沿匹配函数；返回值按
		// Scratch 目标顺序保存，供 OnEngineUpdate 原样派发。
		p.pendingConditions = matchingEventSinks(p.globalSinks(coreevent.BucketCondition), nil)
	}
	if gco == nil {
		read()
	} else {
		gco.RunBetweenScripts(read)
	}
}

// dispatchConditions 启动 sampleConditions 已经选出的条件处理器，不重新求值。
// 先清空 pendingConditions，避免处理器执行或帧重入时重复派发同一批快照。
func (p *scriptEventRegistry) dispatchConditions() {
	sinks := p.pendingConditions
	p.pendingConditions = nil
	if len(sinks) == 0 {
		return
	}
	event := scriptEventDispatch{
		// 条件事件之间不互相等待；每个命中的处理器由独立受管协程执行。
		mode: coroutine.BatchAsync,
		run: func(_ coroutine.Thread, sink *eventSink) {
			sink.Handler.(func())()
		},
	}
	withEventRegistrationBarrier(p.game, func() {
		dispatchMatchedScriptEventBatch(sinks, event)
	})
}
