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

// Join 等待 target 完全退出。
//
// 同一管理器的受管调用方通过 waiterSet + Yield 交还脚本执行权；外部 goroutine 直接
// 阻塞在 target.done。等待自己没有意义，直接返回。直接调用方包括事件注册屏障、
// BatchWaitDone 和停止资源流程。
func (p *Coroutines) Join(target Thread) {
	if target == nil {
		return
	}

	me := p.callerThread()
	if me == nil {
		p.waitOutsideScript(target.done)
		return
	}
	if me == target {
		return
	}
	p.waitOn(me, &target.joinWaiters)
}

// JoinAll 按传入顺序等待全部 Thread 结束。
func (p *Coroutines) JoinAll(targets []Thread) {
	for _, target := range targets {
		p.Join(target)
	}
}

// JoinYieldedOrDone 等待 target 第一次 Yield 或直接结束，不等待其后续生命周期。
//
// 直接调用方：bootstrap/Main 和立即型帧回调；总体用途是让新脚本至少执行一个片段，
// 同时避免它在 Wait/WaitNextFrame 后长期阻塞创建方。
func (p *Coroutines) JoinYieldedOrDone(target Thread) {
	if target == nil {
		return
	}

	me := p.callerThread()
	if me == nil {
		p.waitOutsideScript(target.yieldedOrDone)
		return
	}
	if me == target {
		return
	}
	p.waitOn(me, &target.yieldWaiters)
}
