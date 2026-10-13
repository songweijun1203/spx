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

// Latch 是调度器感知、幂等且只能打开一次的信号闩。
// Wait 会原子发布 Thread 阻塞状态和等待者登记，Open 唤醒当时全部等待者。
type Latch struct {
	manager *Coroutines

	done    chan struct{}
	waiters waiterSet
}

// NewLatch 创建一个尚未打开、绑定当前 Coroutines 的 Latch。
func (p *Coroutines) NewLatch() *Latch {
	return &Latch{
		manager: p,
		done:    make(chan struct{}),
	}
}

// Done 返回在 Latch 打开时关闭的 channel，供 select 或外部 goroutine 使用。
func (p *Latch) Done() <-chan struct{} {
	return p.done
}

// Open 打开 Latch，并恢复所有已登记受管等待者；重复调用安全且无效果。
func (p *Latch) Open() {
	for waiter := range p.waiters.close(p.done) {
		p.manager.markRunnableAndResume(waiter)
	}
}

// Wait 等待 Latch 打开。同一管理器的受管协程 Yield；外部 goroutine 同步阻塞。
func (p *Latch) Wait() {
	manager := p.manager
	me := manager.callerThread()
	if me == nil {
		manager.waitOutsideScript(p.done)
		return
	}

	manager.waitOn(me, &p.waiters)
}
