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

// threadState 是 Update 观察的粗粒度调度状态。
// 它回答“这个 Thread 是否还需要等待某个外部条件”，不负责表示 goroutine
// 是否已经停在条件变量上；后者由 threadImpl.suspendState 精确描述。
type threadState uint8

const (
	// runnable 表示 Thread 不在等待外部条件，可以开始/继续一个脚本片段。
	// 它可能仍在等待 runMu，并不等于当前正在执行。
	threadRunnable threadState = iota
	// blocked 表示 Thread 正在等待 WaitJob、Latch、Join 或 channel 等条件。
	threadBlocked
)

// Sched 让出当前 Thread，并安排一个辅助 goroutine 直接恢复它，不需要等待
// 下一次 Update。它用于单纯把执行机会交给其他脚本，而不建立帧/时间条件。
func (p *Coroutines) Sched(me Thread) {
	// 必须先发布 blocked，再启动快速唤醒；否则辅助 goroutine 可能先 Resume，
	// 随后这里又把状态覆盖成 blocked，造成调度器误判。
	p.setThreadState(me, threadBlocked)
	go p.markRunnableAndResume(me)
	p.Yield(me)
}

// Yield 挂起 me，直到收到 Resume 或取消信号。
//
// 只有当前 goroutine 自己承载、且正持有 runMu 的 Thread 才能调用；对其他
// Thread 调用会 panic。Yield 是所有 Wait、Latch 和 Join 最终释放脚本执行权
// 的公共底层操作。
func (p *Coroutines) Yield(me Thread) {
	// 消息接收者可能从 Before 的 WaitNextFrame、用户 OnMsg 内的 wait，或
	// BroadcastAndWait 的 Join 到达这里。三者都暂停当前调用栈，而非新建 Thread。
	if me == nil || p.callerThread() != me || p.Current() != me {
		panic(ErrCannotYieldANonrunningThread)
	}
	if me.stopAtNextYield.Swap(false) {
		stopThreadIfRunning(me)
	}

	// 第一步：清除“当前执行者”，释放全局脚本执行锁。从这一行开始，其他
	// Thread 才有机会获得 runMu 并执行自己的脚本片段。
	p.setCurrent(nil)
	p.runMu.Unlock()

	// 第二步：把承载当前 Thread 的 Go goroutine 挂起。Resume 可能恰好在
	// runMu.Unlock 与设置 suspended 之间到达，所以 suspendStateSignaled
	// 用来保存“提前到达的唤醒”，避免这个信号丢失。
	me.suspendMu.Lock()
	if me.suspendState == suspendStateSignaled {
		me.suspendState = suspendStateRunning
	} else {
		me.suspendState = suspendStateSuspended
		me.suspended.Store(true)
	}
	me.suspendMu.Unlock()

	for _, waiter := range me.finishYieldWaiters() {
		p.markRunnableAndResume(waiter)
	}

	me.suspendMu.Lock()
	for me.suspendState == suspendStateSuspended && !p.isThreadCanceled(me) {
		me.suspendCond.Wait()
	}
	if me.suspendState == suspendStateSuspended {
		me.suspendState = suspendStateRunning
		me.suspended.Store(false)
	}
	me.suspendMu.Unlock()

	// 第三步：收到 Resume 后这里只是结束条件变量等待。脚本还不能立即继续，
	// 必须重新获得 runMu。于是“收到 Resume 的顺序”和“实际继续执行的顺序”
	// 在旧调度器中仍可能不同。
	p.signalScheduler()
	p.runMu.Lock()
	p.setCurrent(me)
	if me.stopped.Load() {
		panic(ErrAbortThread)
	}
}

// StopAtNextYield 请求 me 在下一次进入 Yield 时取消。
// 只能由当前正在运行的受管 Thread 对自己调用；它允许当前执行片段先完成，
// 用于实现 Scratch 在事件边界附近的停止语义。
func (p *Coroutines) StopAtNextYield(me Thread) {
	if p.currentCoroutineThread() != me {
		panic(ErrCannotYieldANonrunningThread)
	}
	me.stopAtNextYield.Store(true)
}

// Resume 唤醒处于 suspended 的 me。若 Yield 尚未来得及正式发布 suspended，
// Resume 会把状态记成 signaled，随后 Yield 消费该信号而不睡眠。
func (p *Coroutines) Resume(me Thread) {
	// Resume 只改变挂起状态并发送条件变量信号，不会把 runMu 直接交给 me。
	// 被唤醒的 Go goroutine 最终仍会在 Yield 尾部的 runMu.Lock 处竞争执行权。
	me.suspendMu.Lock()
	defer me.suspendMu.Unlock()
	if p.isThreadCanceled(me) {
		return
	}

	switch me.suspendState {
	case suspendStateSuspended:
		me.suspendState = suspendStateRunning
		me.suspended.Store(false)
		me.suspendCond.Signal()
	case suspendStateRunning:
		me.suspendState = suspendStateSignaled
	}
}

// Current 返回当前持有 runMu、正在执行脚本片段的 Thread；没有脚本执行时
// 返回 nil。它是调度器全局状态，不适合用来识别任意调用 goroutine 的身份。
func (p *Coroutines) Current() Thread {
	return p.current.Load()
}

// setCurrent 发布当前 runMu owner。调用方必须已经取得或正在释放 runMu，
// 以保证 current 与实际脚本执行权一致。
func (p *Coroutines) setCurrent(th Thread) {
	p.current.Store(th)
}

// markRunnableAndResume 原子地把 th 改为 runnable、通知 Update 状态变化，
// 再发送 goroutine 级 Resume 信号。状态必须先发布，避免醒来的 Thread 已经
// 运行，而 Update 仍把它当成 blocked。
func (p *Coroutines) markRunnableAndResume(th Thread) {
	// 先让 Update 能看到 runnable 状态，再发送 Resume。注意 runnable 只是
	// “有资格继续”；直到该 goroutine 真正获得 runMu，用户脚本都还没执行。
	p.schedulerMu.Lock()
	if p.isThreadCanceled(th) {
		p.schedulerMu.Unlock()
		return
	}
	p.setThreadStateLocked(th, threadRunnable)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
	p.Resume(th)
}

// isThreadCanceled 同时检查 stopped 原子标志和 context 取消信号。
// nil 表示没有关联 Thread，例如某些主线程任务，因此返回 false。
func (p *Coroutines) isThreadCanceled(th Thread) bool {
	if th == nil {
		return false
	}
	if th.stopped.Load() {
		return true
	}
	select {
	case <-th.Context().Done():
		return true
	default:
		return false
	}
}

// signalScheduler 唤醒一个正在 schedulerCond.Wait 的 Update 循环，使其重新
// 检查任务队列和所有 Thread 状态。
func (p *Coroutines) signalScheduler() {
	p.schedulerMu.Lock()
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// setThreadState 在 schedulerMu 下修改 th 的调度状态并通知 Update。
func (p *Coroutines) setThreadState(th Thread, state threadState) {
	p.schedulerMu.Lock()
	p.setThreadStateLocked(th, state)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// setThreadStateLocked 修改状态但不自行加锁/通知。调用者必须持有 schedulerMu，
// 它用于把状态变化与 WaitJob 入队或 waiter 登记组合成一个原子操作。
func (p *Coroutines) setThreadStateLocked(th Thread, state threadState) {
	p.threadStates[th] = state
}

// removeThreadState 在 Thread 永久结束时把它移出调度状态表，并唤醒可能正在
// 等待“所有 runnable Thread 都让出”的 Update。
func (p *Coroutines) removeThreadState(th Thread) {
	p.schedulerMu.Lock()
	delete(p.threadStates, th)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// runnableThreadCountLocked 返回无需新的外部事件就能继续推进的 Thread 数量。
// 调用者必须持有 schedulerMu。
func (p *Coroutines) runnableThreadCountLocked() int {
	// 包括已创建但尚未首次拿到 runMu 的 Thread，以及已 Resume、正在等待
	// 重新取得 runMu 的 Thread；不只统计 Current() 指向的正在执行者。
	runnable := 0
	for th, state := range p.threadStates {
		if state == threadRunnable && !p.isThreadCanceled(th) {
			runnable++
		}
	}
	return runnable
}
