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

// Latch 是调度器感知的、幂等的一次性信号。
// Wait 会原子地发布 Thread 的阻塞状态和等待者登记。
//
// Latch 类似只会打开一次的门。它与普通 channel 等待的区别是：若调用者是
// 受管理的脚本 Thread，Wait 会先把 Thread 标为 blocked，再通过 Yield 释放
// runMu；Open 则用 markRunnableAndResume 把等待者送回协程调度流程。
// StartBatch 用多个 Latch 串成接力链，控制一批事件处理函数的首次运行顺序。
type Latch struct {
	// manager 是拥有该 Latch 的协程管理器，用于识别当前 Thread 和恢复等待者。
	manager *Coroutines

	// mu 保护 opened 与 waiters，也保证 Open 和 Wait 登记不会互相错过。
	mu sync.Mutex
	// opened 表示门闩是否已经永久打开；一旦为 true 就不会再关闭。
	opened bool
	// done 在首次 Open 时关闭，供非受管理 goroutine 以及 select 监听完成信号。
	done chan struct{}
	// waiters 保存已阻塞、等待 Open 唤醒的受管理 Thread 集合。
	waiters map[Thread]struct{}
}

// NewLatch 创建一个属于 p、初始处于关闭状态的 Latch。
func (p *Coroutines) NewLatch() *Latch {
	return &Latch{
		manager: p,
		done:    make(chan struct{}),
	}
}

// Done 返回只读完成通道；Latch 首次打开时该通道会被关闭。
// 因为关闭通道会永久广播，所以在 Open 之后开始监听也能立即收到信号。
func (p *Latch) Done() <-chan struct{} {
	return p.done
}

// Open 永久打开 Latch，并恢复当时登记的所有等待者。
// 重复调用是安全的，第一次之后的调用不会产生任何效果。
func (p *Latch) Open() {
	p.mu.Lock()
	if p.opened {
		p.mu.Unlock()
		return
	}
	p.opened = true
	// 关闭 done 是一次性广播：已经在 select/接收中等待的 relay 会醒来，之后
	// 才调用 Done 的代码也能立即观察到 Latch 已打开。
	close(p.done)
	waiters := copyThreadSet(p.waiters)
	p.waiters = nil
	p.mu.Unlock()

	// [消息分发 9/11] Open 只让等待者变为 runnable 并发送 Resume，不会把
	// runMu 直接交给它。当前广播/前一个 handler 若仍持有 runMu，被唤醒的
	// 接收者只能等当前执行片段 Yield 或结束后再继续。
	for _, waiter := range waiters {
		p.manager.markRunnableAndResume(waiter)
	}
}

// Wait 暂停当前受管理 Thread，直到 Latch 打开。
//
// 如果调用者不属于该 Latch 的 Coroutines，方法不会操作脚本调度状态，而是
// 直接阻塞调用它的 Go goroutine，等待 done 通道关闭。
func (p *Latch) Wait() {
	manager := p.manager
	// 通过 goroutine 身份映射取得正在执行 StartBatch 包装函数的接收者 Thread。
	me := manager.currentCoroutineThread()
	if me == nil {
		<-p.done
		return
	}

	// “登记为 waiter”和“标为 blocked”必须一起发布，避免 Open 恰好夹在两步
	// 之间，造成 Thread 已阻塞却错过唤醒。
	manager.schedulerMu.Lock()
	manager.setThreadStateLocked(me, threadBlocked)
	p.mu.Lock()
	// [消息分发 11] 第一个任务看到 progress[0] 已打开，可直接通过；如果某个
	// 后续 goroutine 抢先取得 runMu，它看到自己的接力棒尚未打开，就登记等待。
	registered := !p.opened
	if registered {
		if p.waiters == nil {
			p.waiters = make(map[Thread]struct{})
		}
		p.waiters[me] = struct{}{}
	}
	p.mu.Unlock()
	if !registered {
		manager.setThreadStateLocked(me, threadRunnable)
	}
	manager.schedulerCond.Signal()
	manager.schedulerMu.Unlock()

	if registered {
		defer p.removeWaiter(me)
		// 只有真正等待了一个尚未打开的 latch 才需要 Yield。若 latch 已打开，
		// 当前 Thread 保持 runMu，可以直接继续执行。
		// Yield 释放 runMu。前一个接收者稍后 next.Open 会 Resume 当前 Thread；
		// 它重新取得 runMu 后从这里返回，再进入 task.Run。
		manager.Yield(me)
	}
}

// removeWaiter 从等待集合清除 waiter。
// Wait 在恢复后延迟调用它，以覆盖正常 Open、取消以及虚假/重复恢复等路径。
func (p *Latch) removeWaiter(waiter Thread) {
	p.mu.Lock()
	delete(p.waiters, waiter)
	if len(p.waiters) == 0 {
		p.waiters = nil
	}
	p.mu.Unlock()
}
