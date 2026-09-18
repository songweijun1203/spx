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

import "github.com/visualfc/gid"

type threadState uint8

const (
	threadRunnable threadState = iota
	threadBlocked
)

// Sched 让出当前协程，并安排它在不经过 Update 任务队列的情况下恢复。
func (p *Coroutines) Sched(me Thread) {
	p.requireCurrent(me)
	// 先发布 blocked 状态，避免随后快速发生的唤醒被漏掉。
	p.setThreadState(me, threadBlocked)
	go p.markRunnableAndResume(me)
	p.Yield(me)
}

// Yield 协作式地让出当前脚本 Thread。正常情况下，它一直停在本函数内部，直到
// Resume 唤醒它并且它重新取得 runMu；只有这条正常恢复路径会让 Yield 返回。
// 如果 Thread 已被 Cancel，Yield 不会返回用户脚本，而是在取得 runMu 后抛出
// ErrAbortThread，通过 finishThread 彻底终止并清理该 Thread。
//
// 这里涉及两套相互独立的同步机制：
//
//  1. p.runMu 是所有脚本 Thread 共享的执行权锁。同一时刻只有持有它的 Thread
//     可以执行用户脚本；让出时释放，恢复时必须重新竞争；
//  2. me.suspendMu + me.suspendCond 只属于当前 Thread，用来记录 Resume 是否已经
//     到达，以及让当前 goroutine 睡眠/唤醒。Cond 的锁不是脚本执行权锁。
//
// 如果 me 不是调用 goroutine 对应的 Thread，或者 me 当前没有持有 runMu，说明
// 调用者试图挂起一个并非正在运行的协程，requireCurrent 会 panic。
func (p *Coroutines) Yield(me Thread) {
	// [阶段 1：验证调用身份]
	// 必须同时满足 callerThread()==me 且 Current()==me。前者确认“哪个 goroutine
	// 在调用”，后者确认“哪个 Thread 正持有调度器授予的脚本执行权”。
	p.requireCurrent(me)

	// [停止预约：在本次 Yield 彻底终止 Thread]
	// stopAtNextYield 不是“Thread 已经停止”的状态，而是“到达下一次 Yield 时必须
	// 彻底终止”的一次性预约。Swap(false) 原子地读取并清除预约：
	//   - 返回 false：没有停止预约，继续走普通挂起/Resume 流程；
	//   - 返回 true：本次 Yield 就是终止边界，立即调用 Cancel 把 stopped 置为 true。
	//
	// Cancel 后不会跳过本次挂起继续执行 Yield 后面的用户代码。当前 goroutine 会
	// 完成释放 runMu、发布首次让出等必要步骤；因为已经取消，它不会等待正常
	// Resume，取得 runMu 后会在函数末尾抛出 ErrAbortThread，整个 Thread 随即退出。
	if me.stopAtNextYield.Swap(false) {
		me.Cancel()
	}

	// [阶段 2：交还全局脚本执行权]
	// 必须在下面第一次获取 me.suspendMu 之前先释放 runMu，不能简单把 Unlock 挪到
	// suspendCond.Wait 附近。原因是 suspendMu 可能正被 Resume/Cancel 持有；如果 me
	// 一边持有全局 runMu，一边阻塞等待自己的 suspendMu，所有其他脚本 Thread 都会
	// 被挡在 runMu 外面，严重时会和依赖脚本继续推进的唤醒路径形成锁等待环。
	//
	// 另外，提前 Resume 或 Cancel 会令阶段 5 根本不执行 Cond.Wait；如果只在 Wait
	// 前后释放/重取 runMu，这些分支可能没有释放就再次 Lock，直接把自己锁死。
	// 因此这里把“交还脚本执行权”作为无条件动作：先清空 current，再释放 runMu。
	// 其他已就绪 Thread 从此可以执行，me 则只继续处理自己的挂起登记。
	// 此时 me 尚未进入 Cond.Wait；清空 current 只表示它从现在起不再拥有脚本执行权。
	p.setCurrent(nil)
	p.runMu.Unlock()

	// [阶段 3：发布当前 Thread 的挂起状态]
	// suspendStateRunning 不仅表示 Thread 正在执行，也作为“尚未正式进入睡眠”的
	// 初始状态。释放 runMu 后、取得 suspendMu 前存在一个时间窗口，Resume 可能
	// 已经先到。Resume 遇到 Running 时不会 Signal 一个尚未 Wait 的条件变量，
	// 而是把状态记为 Signaled；这样唤醒许可不会因为 Signal 早于 Wait 而丢失。
	me.suspendMu.Lock()
	if me.suspendState == suspendStateSignaled {
		// Resume 已经提前到达：消费这次预存的唤醒，保持 Running。后面的等待循环
		// 将不会进入 Cond.Wait，但 me 仍然必须在阶段 6 重新竞争 runMu。
		me.suspendState = suspendStateRunning
	} else {
		// 尚无 Resume：正式发布 Suspended。此后 Resume 会把状态改成 Running，
		// 并对 suspendCond 调用 Signal。
		me.suspendState = suspendStateSuspended
	}
	me.suspendMu.Unlock()

	// [阶段 4：通知等待“首次让出或结束”的 Thread]
	// CreateAndStart/JoinYieldedOrDone 可以等待目标 Thread 第一次 Yield。close 会：
	//   - 原子地关闭这个一次性 waiterSet；
	//   - 关闭 yieldedOrDone channel，通知协程外的等待者；
	//   - 取出协程内登记的等待 Thread，并清空集合。
	//
	// 必须在 waiterSet 自己的锁之外逐个唤醒，避免唤醒路径反过来取得相关锁造成
	// 死锁。markRunnableAndResume 先把等待者标为 runnable，再发送它自己的 Resume。
	for waiter := range me.yieldWaiters.close(me.yieldedOrDone) {
		p.markRunnableAndResume(waiter)
	}

	// [阶段 5：正常情况等待 Resume；取消情况直接进入终止路径]
	// Cond.Wait 要求调用者持有 suspendMu。每次 Wait 都会原子地：
	//   1. 释放 suspendMu；
	//   2. 让当前 goroutine 睡眠；
	//   3. 被 Signal 后重新取得 suspendMu；
	//   4. 然后才从 Wait 返回。
	//
	// 条件变量允许虚假唤醒，而且 Cancel 也会 Signal，因此这里必须用 for 重新检查
	// 完整谓词。只有状态不再是 Suspended，或者 Thread 已取消，才能离开等待。
	// 如果本次 Yield 在开头消费了 stopAtNextYield，isThreadCanceled 已经为 true，
	// 所以这里根本不会调用 Cond.Wait；但它接下来走的是终止路径，不是正常返回。
	me.suspendMu.Lock()
	for me.suspendState == suspendStateSuspended && !p.isThreadCanceled(me) {
		me.suspendCond.Wait()
	}
	// Cancel 可能在没有正常 Resume 的情况下把等待唤醒，此时状态仍为 Suspended。
	// 将它恢复成 Running 只是收拢内部状态；阶段 6 取得 runMu 后仍会根据 stopped
	// 抛出 ErrAbortThread，用户脚本不会真的继续执行。
	if me.suspendState == suspendStateSuspended {
		me.suspendState = suspendStateRunning
	}
	me.suspendMu.Unlock()

	// [阶段 6：重新取得全局串行锁，然后选择“正常返回”或“终止 Thread”]
	// suspendCond 的 Signal 只允许 goroutine 离开上面的等待，不会转交 runMu，也
	// 不保证它立即被 Go 调度器执行。这里的 signalScheduler 不唤醒脚本，也不修改
	// runnableThreads/currentJobs；它只是防御性地唤醒可能停在 nextUpdateAction 的
	// Update，让 Update 重新检查谓词。
	//
	// 正常 WaitJob 路径通常已经在 markRunnableAndResume 中通知过一次，Thread 下一次
	// blocked/结束时也会再通知；所以这里经常只是一次无害的重复通知。它仍覆盖了
	// 直接 Resume、Cancel 等没有在同一位置发布队列变化的路径，避免 Update 必须等到
	// 更晚的 blocked/remove/入队通知才知道该 Thread 的挂起流程已经向前推进。
	// 通知之后，当前 Thread 仍必须和其他已恢复 Thread 一起竞争全局 runMu。
	p.signalScheduler()
	// 这行可能再次阻塞。正常恢复的 Thread 只有 Lock 成功后才能继续用户脚本；
	// 已取消的 Thread 取得它只是为了在相同的串行约束下进入统一终止/清理路径。
	p.runMu.Lock()
	// 重新发布全局所有者。此时 Current() 才再次返回 me。
	p.setCurrent(me)

	// [终止分支]
	// stopped 可能由开头消费 stopAtNextYield 后的 Cancel 设置，也可能由 Thread
	// 挂起期间发生的外部 Cancel 设置。两种情况都在这里抛出 ErrAbortThread，并由
	// runThread 的 defer 进入 finishThread；Yield 不会返回，后续用户代码不会执行。
	// 必须先取得 runMu 再退出，保证清理过程仍遵守脚本串行约束，并让 finishThread
	// 按正常生命周期释放 runMu、关闭 done、唤醒 Join 等待者并注销 Thread。
	if me.stopped.Load() {
		panic(ErrAbortThread)
	}
	// [正常恢复分支]
	// 只有 stopped==false 才能到达这里。Yield 正常返回，上层脚本从让出点之后继续。
}

// Resume 唤醒已经挂起的 me。如果 Yield 还没来得及发布挂起状态，则先记录一个
// signal，让稍后进入 Yield 的 Thread 消费它并跳过睡眠，避免丢失唤醒。
func (p *Coroutines) Resume(me Thread) {
	me.suspendMu.Lock()
	defer me.suspendMu.Unlock()
	if p.isThreadCanceled(me) {
		return
	}

	switch me.suspendState {
	case suspendStateSuspended:
		me.suspendState = suspendStateRunning
		me.suspendCond.Signal() //通知协程
	case suspendStateRunning:
		me.suspendState = suspendStateSignaled
	}
}

// StopAtNextYield 预约在 me 的下一次 Yield 边界彻底终止该 Thread。
//
// 本函数只设置一次性预约，不会立刻 Cancel，也不会异步抢占正在执行的用户代码：
// me 可以继续执行当前连续脚本片段；一旦进入 Yield，预约会被消费，Yield 将调用
// Cancel 并通过 ErrAbortThread/finishThread 退出，不会返回后续用户代码。如果 me
// 在到达任何 Yield 之前自然 return，那么 Thread 正常结束，预约不会再生效。
// 本函数只能由当前正在运行且受该调度器管理的 me 自己调用。
func (p *Coroutines) StopAtNextYield(me Thread) {
	p.requireCurrent(me)
	me.stopAtNextYield.Store(true)
}

// IsInCoroutine 报告调用者是否正在本调度器管理的协程中运行。
func (p *Coroutines) IsInCoroutine() bool {
	return p.callerThread() != nil
}

// Current 返回当前持有脚本执行权的协程；没有时返回 nil。
func (p *Coroutines) Current() Thread {
	return p.current.Load()
}

// callerThread 根据 goroutine ID 找到调用者所属的 Thread；Current 表示 runMu
// 当前授予执行权的 Thread。两者相同才允许执行 Yield 等当前协程操作。
func (p *Coroutines) callerThread() Thread {
	value, ok := p.goroutineThreads.Load(gid.Get())
	if !ok {
		return nil
	}
	return value.(Thread)
}

func (p *Coroutines) requireCurrent(me Thread) {
	if me == nil || p.callerThread() != me || p.Current() != me {
		panic(ErrCannotYieldANonrunningThread)
	}
}

func (p *Coroutines) setCurrent(th Thread) {
	p.current.Store(th)
}

func (p *Coroutines) markRunnableAndResume(th Thread) {
	// 先在 schedulerMu 保护下把 Thread 标为 runnable，让 Update 能观察到它，
	// 再发送 Resume 信号。收到信号不等于脚本已经运行；它仍需重新取得 runMu。
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

func (p *Coroutines) signalScheduler() {
	p.schedulerMu.Lock()
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

func (p *Coroutines) setThreadState(th Thread, state threadState) {
	p.schedulerMu.Lock()
	p.setThreadStateLocked(th, state)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

func (p *Coroutines) setThreadStateLocked(th Thread, state threadState) {
	if state == threadRunnable {
		p.runnableThreads[th] = struct{}{}
	} else {
		delete(p.runnableThreads, th)
	}
}

func (p *Coroutines) removeThreadState(th Thread) {
	p.schedulerMu.Lock()
	delete(p.runnableThreads, th)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// 调用者必须已经持有 schedulerMu。
func (p *Coroutines) hasRunnableThreadLocked() bool {
	for th := range p.runnableThreads {
		if !p.isThreadCanceled(th) {
			return true
		}
	}
	return false
}
