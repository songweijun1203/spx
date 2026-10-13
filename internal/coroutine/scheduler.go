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

// 本文件实现脚本执行权的核心握手：
//
//	运行中的 Thread 持有 runMu
//	  -> Yield 清除 current 并释放 runMu
//	  -> suspendCond 挂起承载它的 Go goroutine
//	  -> Resume/Cancel 唤醒 goroutine
//	  -> goroutine 重新取得 runMu、设置 current 后继续或退出
//
// runnableThreads 则供 Update 判断“是否仍有脚本即将/正在执行”，不直接负责 goroutine
// 的休眠。因此恢复必须先发布 runnable，再调用 Resume，避免 Update 先行返回。

// threadState 表示调度器视角下协程是否可以继续执行。
// 它只用于维护 runnableThreads，与 Thread 内部记录实际挂起状态的
// suspendState 是两套不同层次的状态。
type threadState uint8

const (
	// threadRunnable 表示协程已经具备运行条件，Update 需要等待它完成
	// 当前脚本执行片段，或者再次主动让出执行权。
	threadRunnable threadState = iota
	// threadBlocked 表示协程正在等待外部条件，不应阻止本轮 Update 返回。
	threadBlocked
)

// Sched 主动让出当前协程的执行权，并安排它在不经过下一轮 Update 的情况下恢复。
// 它相当于一次仅用于协程间公平调度的“短暂让出”，不会等待帧事件或定时任务。
func (p *Coroutines) Sched(me Thread) {
	p.requireCurrent(me)
	// 必须先发布阻塞状态，再启动恢复流程。否则恢复动作执行得足够快时，
	// 后续的 Yield 可能再次把协程挂起，造成唤醒信号丢失。
	p.setThreadState(me, threadBlocked)
	go p.markRunnableAndResume(me)
	p.Yield(me)
}

// Yield 挂起当前协程，直到它被 Resume 或 Cancel 唤醒。
// 调用者必须就是当前持有脚本执行权的协程，否则会 panic。
func (p *Coroutines) Yield(me Thread) {
	p.requireCurrent(me)
	// StopAtNextYield 只负责设置标记；真正的取消发生在协程到达这里时。
	if me.stopAtNextYield.Swap(false) {
		me.Cancel()
	}

	// 清除当前协程并释放 runMu，让其他受管协程能够执行脚本切片。
	p.setCurrent(nil)
	p.runMu.Unlock()

	// 在通知其他等待者前发布挂起状态。Resume 可能比这里更早到达；
	// suspendStateSignaled 用于记住这次提前到达的唤醒，避免随后错误地阻塞。
	me.suspendMu.Lock()
	if me.suspendState == suspendStateSignaled {
		// 提前到达的 Resume 已经被记录，本次 Yield 不再真正休眠。
		me.suspendState = suspendStateRunning
	} else {
		me.suspendState = suspendStateSuspended
	}
	me.suspendMu.Unlock()

	// 唤醒所有等待 me 首次让出或结束的协程。
	for waiter := range me.yieldWaiters.close(me.yieldedOrDone) {
		p.markRunnableAndResume(waiter)
	}

	// suspendCond 只控制当前 Go 协程是否休眠；逻辑上是否可运行，
	// 则由 runnableThreads 单独维护。
	me.suspendMu.Lock()
	for me.suspendState == suspendStateSuspended && !p.isThreadCanceled(me) {
		me.suspendCond.Wait() // 挂起当前 Go 协程，并在等待期间释放 suspendMu。
	}
	if me.suspendState == suspendStateSuspended {
		// 协程因取消而退出等待时，将底层挂起状态恢复为 running。
		me.suspendState = suspendStateRunning
	}
	me.suspendMu.Unlock()

	// 通知正在等待调度状态变化的 Update，然后重新竞争唯一的脚本执行权。
	p.signalScheduler()
	p.runMu.Lock()
	p.setCurrent(me)
	// Cancel 会唤醒挂起的协程；协程重新取得执行权后，通过 panic 结束自身。
	if me.stopped.Load() {
		panic(ErrAbortThread)
	}
}

// Resume 唤醒已经挂起的 me。如果 Yield 还没来得及发布挂起状态，
// 就先记录 suspendStateSignaled，供随后的 Yield 消费，避免丢失唤醒。
func (p *Coroutines) Resume(me Thread) {
	me.suspendMu.Lock()
	defer me.suspendMu.Unlock()
	// 已取消的协程不会再次进入正常执行流程。
	if p.isThreadCanceled(me) {
		return
	}

	switch me.suspendState {
	case suspendStateSuspended:
		// 协程已经进入 Cond.Wait，可以直接发信号唤醒。
		me.suspendState = suspendStateRunning
		me.suspendCond.Signal()
	case suspendStateRunning:
		// Yield 尚未真正挂起，先保存唤醒信号，交给随后的 Yield 消费。
		me.suspendState = suspendStateSignaled
	}
}

// StopAtNextYield 标记当前协程，使它在下一次 Yield 时取消。
// 调用者必须是当前正在运行并持有脚本执行权的受管协程。
func (p *Coroutines) StopAtNextYield(me Thread) {
	p.requireCurrent(me)
	me.stopAtNextYield.Store(true)
}

// IsInCoroutine 返回调用方所在的 Go 协程是否由当前 Coroutines 管理。
func (p *Coroutines) IsInCoroutine() bool {
	return p.callerThread() != nil
}

// Current 返回当前持有 runMu、拥有脚本执行权的协程；没有时返回 nil。
func (p *Coroutines) Current() Thread {
	return p.current.Load()
}

// callerThread 根据调用方的 Go 协程 ID 查找对应的受管协程。
// 它回答“调用者是谁”，而 Current 回答“当前脚本执行权属于谁”。
func (p *Coroutines) callerThread() Thread {
	value, ok := p.goroutineThreads.Load(gid.Get())
	if !ok {
		return nil
	}
	return value.(Thread)
}

// requireCurrent 校验 me 既是调用方对应的受管协程，也是当前执行权持有者。
// 这可以防止普通 Go 协程或非当前协程错误地调用 Yield 等调度操作。
func (p *Coroutines) requireCurrent(me Thread) {
	if me == nil || p.callerThread() != me || p.Current() != me {
		panic(ErrCannotYieldANonrunningThread)
	}
}

// setCurrent 更新当前脚本执行权的持有者。
func (p *Coroutines) setCurrent(th Thread) {
	p.current.Store(th)
}

// markRunnableAndResume 先把协程标记为逻辑可运行，再唤醒其底层 Go 协程。
// 这个顺序保证 Update 在协程恢复执行前就能观察到它，因而不会提前返回。
func (p *Coroutines) markRunnableAndResume(th Thread) {
	// 先把调度器中的线程状态改为可运行，再发出底层恢复信号。
	// WaitForChan 的 worker 会直接走到这里，不经过 WaitJob/Update 队列。
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

// isThreadCanceled 返回协程是否已被显式停止，或其 Context 是否已取消。
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

// signalScheduler 唤醒等待 schedulerCond 的调度流程，使其重新检查当前状态。
func (p *Coroutines) signalScheduler() {
	p.schedulerMu.Lock()
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// setThreadState 加锁更新协程的逻辑调度状态，并通知等待状态变化的调度流程。
func (p *Coroutines) setThreadState(th Thread, state threadState) {
	p.schedulerMu.Lock()
	p.setThreadStateLocked(th, state)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// setThreadStateLocked 更新 runnableThreads；调用者必须已经持有 schedulerMu。
// runnableThreads 只保存可运行协程，阻塞状态通过从集合中删除来表达。
func (p *Coroutines) setThreadStateLocked(th Thread, state threadState) {
	if state == threadRunnable {
		p.runnableThreads[th] = struct{}{}
	} else {
		delete(p.runnableThreads, th)
	}
}

// removeThreadState 在协程结束时将它彻底移出可运行集合，并唤醒调度流程。
func (p *Coroutines) removeThreadState(th Thread) {
	p.schedulerMu.Lock()
	delete(p.runnableThreads, th)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// hasRunnableThreadLocked 返回当前是否还存在未取消的可运行协程。
// 调用者必须已经持有 schedulerMu。
func (p *Coroutines) hasRunnableThreadLocked() bool {
	for th := range p.runnableThreads {
		if !p.isThreadCanceled(th) {
			return true
		}
	}
	return false
}
