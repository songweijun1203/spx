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

package coroutine

import "sync"

// HandlerPolicy 决定同一个事件注册项尚在执行时，新一次触发如何处理。
type HandlerPolicy uint8

const (
	// RestartExisting 取消旧执行并接纳新执行；新 Thread 继承旧执行的排序位置。
	RestartExisting HandlerPolicy = iota
	// IgnoreWhileRunning 保留旧执行并取消本次新 Thread。
	IgnoreWhileRunning
)

// HandlerState 保存单个事件注册项当前正在运行的 Thread 和重入策略。
// 首次使用后不得复制；不同克隆精灵必须重新创建自己的 HandlerState。
type HandlerState struct {
	mu     sync.Mutex
	active Thread
	policy HandlerPolicy
}

// NewHandlerState 为指定策略创建尚未运行的处理器状态。
func NewHandlerState(policy HandlerPolicy) HandlerState {
	return HandlerState{policy: policy}
}

// Start 决定本次处理器 Thread 是接纳、重启旧执行还是忽略，并返回退出 cleanup。
//
// 直接调用方：事件 Task.Setup；总体流程调用方：脚本事件批量派发。cleanup 通过比较
// active == thread，保证旧执行迟到的退出不会清除已经替换上来的新执行状态。
func (p *HandlerState) Start(thread Thread) func() {
	p.mu.Lock()
	previous := p.active
	if p.policy == IgnoreWhileRunning && previous != nil && !previous.Stopped() {
		p.mu.Unlock()
		if thread != nil {
			thread.Cancel()
		}
		return nil
	}
	if p.policy == RestartExisting && thread != nil && previous != nil &&
		!previous.Stopped() && previous.Context().Err() == nil {
		thread.resumeOrder = previous.resumeOrder
	}
	p.active = thread
	p.mu.Unlock()

	if previous != nil && previous != thread {
		previous.Cancel()
	}
	return func() {
		p.mu.Lock()
		if p.active == thread {
			p.active = nil
		}
		p.mu.Unlock()
	}
}
