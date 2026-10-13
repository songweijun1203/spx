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

import (
	stime "time"
)

// StopCurrent 通过 ErrAbortThread 立即展开当前 Thread 的调用栈。
// 直接调用方：业务 Stop/删除等控制流；最终由 runThread 的 recover 统一收尾。
func (p *Coroutines) StopCurrent() {
	panic(ErrAbortThread)
}

// StopThisScript 抛出 Scratch “停止这个脚本”哨兵，由最近的过程或事件边界截获。
// 过程边界可返回调用者；事件边界则结束整个 Thread。
func (p *Coroutines) StopThisScript() {
	panic(ErrStopThisScript)
}

// Stop 请求取消指定 Thread；传 nil 或重复调用均安全。
func (p *Coroutines) Stop(thread Thread) {
	if thread != nil {
		thread.Cancel()
	}
}

// StopIf 取消所有满足 filter 的已登记 Thread。
// filter 在生命周期锁外执行，允许其中读取业务对象或调用其他运行时方法而不死锁。
func (p *Coroutines) StopIf(filter func(th Thread) bool) {
	allThreads := p.snapshotThreads()
	threads := allThreads[:0]
	for _, th := range allThreads {
		if filter(th) {
			threads = append(threads, th)
		}
	}
	for _, th := range threads {
		th.Cancel()
	}
}

// StopAll 请求取消当前全部 Thread，但不等待它们完成 cleanup 或外部 worker 退出。
//
// 直接调用方：游戏 Stop(AllStop) 等快速停止路径。准入在取消快照期间关闭；没有未完成
// Setup 且不处于完整排空阶段时立即重新开放，允许随后创建新一轮脚本。
func (p *Coroutines) StopAll() {
	p.admissionMu.Lock()
	p.closeAdmissionLocked()
	p.cancelAllLocked()
	if !p.stopping && p.pendingSetups.Load() == 0 {
		p.openAdmissionLocked()
	}
	p.admissionMu.Unlock()
}

// StopAllAndWait 取消所有脚本，并等待 Thread cleanup 和 WaitToDo worker 退出。
//
// 受管调用方自身不计入等待，并临时释放 runMu 让其他 Thread 完成退出；外部调用方走
// RunAfterStopAll。timeout <= 0 表示无限等待。运行时 callback 不能排空自身，否则以
// ErrReentrantWait 拒绝。
func (p *Coroutines) StopAllAndWait(timeout stime.Duration) bool {
	p.requireDrainCaller()
	caller := p.callerThread()
	if caller != nil {
		p.StopAll()
		return p.waitForPeerDrain(timeout, caller)
	}
	return p.RunAfterStopAll(timeout, nil)
}

// RunAfterStopAll 从外部停止并排空所有脚本/worker，然后在准入仍关闭时执行 call。
//
// 直接调用方：引擎退出、reset、reload；总体流程调用方：游戏生命周期切换。必须在
// 受管 Thread 和运行时 callback 外调用。timeout 覆盖停止锁获取和排空，不限制 call；
// 超时会保持准入关闭，直到以后一次成功排空，防止旧任务与新生命周期交叉。
func (p *Coroutines) RunAfterStopAll(timeout stime.Duration, call func()) bool {
	_, completed := p.RunAfterStopAllIf(timeout, nil, call)
	return completed
}

// RunAfterStopAllIf 在取得全局停止屏障后重新检查 condition，再决定是否排空并执行 call。
// selected=false 表示条件失效或没能及时取得屏障；completed=false 表示排空未完成。
func (p *Coroutines) RunAfterStopAllIf(timeout stime.Duration, condition func() bool, call func()) (selected, completed bool) {
	p.requireDrainCaller()
	if p.callerThread() != nil {
		panic("coroutine: RunAfterStopAll requires an external caller")
	}
	remaining, locked := p.lockShutdown(timeout)
	if !locked {
		return false, false
	}
	defer p.shutdownMu.Unlock()
	if condition != nil && !condition() {
		return false, false
	}

	p.admissionMu.Lock()
	p.beginStoppingLocked()
	p.admissionMu.Unlock()
	if !p.waitForDrain(remaining, nil) {
		return true, false
	}
	defer func() {
		p.admissionMu.Lock()
		p.endStoppingLocked()
		p.admissionMu.Unlock()
	}()
	if call != nil {
		id, previous := p.enterCallback(callbackShutdown)
		defer p.leaveCallback(id, previous)
		call()
	}
	return true, true
}

// beginStoppingLocked 关闭准入并取消当前全部 Thread；调用者必须持有 admissionMu 写锁。
func (p *Coroutines) beginStoppingLocked() {
	if !p.stopping {
		p.stopping = true
		p.closeAdmissionLocked()
	}
	p.cancelAllLocked()
}

// endStoppingLocked 在成功排空和清理后重新开放准入。
func (p *Coroutines) endStoppingLocked() {
	if !p.stopping {
		return
	}
	p.openAdmissionLocked()
	p.stopping = false
}

// cancelAllLocked 对当前 Thread 快照发送取消；名称沿用历史，函数内部不持 admissionMu。
func (p *Coroutines) cancelAllLocked() {
	for _, th := range p.snapshotThreads() {
		th.Cancel()
	}
}

// waitForPeerDrain 供受管 Thread 停止同伴时使用。
// 它临时交还 runMu，使被取消的同伴能够取得执行权并执行 defer/cleanup。
func (p *Coroutines) waitForPeerDrain(timeout stime.Duration, caller Thread) bool {
	// 释放 runMu，使已取消的同伴能够完成注销。
	p.setCurrent(nil)
	p.runMu.Unlock()
	completed := p.waitForDrain(timeout, caller)
	p.runMu.Lock()
	p.setCurrent(caller)
	return completed
}

// waitForDrain 等到除 skip 外的全部 Thread 和所有外部 worker 都退出。
// predicate 检查和 lifecycleChanged 订阅在同一把 threadsMu 下完成，避免丢失完成通知。
func (p *Coroutines) waitForDrain(timeout stime.Duration, skip Thread) bool {
	var timedOut <-chan stime.Time
	if timeout > 0 {
		timer := stime.NewTimer(timeout)
		defer timer.Stop()
		timedOut = timer.C
	}

	for {
		p.threadsMu.Lock()
		if !p.hasPendingWorkLocked(skip) {
			p.threadsMu.Unlock()
			return true
		}
		// 在 predicate 锁内订阅，确保检查与订阅之间不会漏掉退出事件。
		if p.lifecycleChanged == nil {
			p.lifecycleChanged = make(chan struct{})
		}
		changed := p.lifecycleChanged
		p.threadsMu.Unlock()

		if !p.waitForDrainChange(changed, timedOut) {
			// 完成可能与超时同时发生；最终以生命周期注册表为准。
			return !p.hasPendingWork(skip)
		}
	}
}

// hasPendingWork 加锁判断是否仍有待排空 Thread 或 worker。
func (p *Coroutines) hasPendingWork(skip Thread) bool {
	p.threadsMu.Lock()
	defer p.threadsMu.Unlock()
	return p.hasPendingWorkLocked(skip)
}

// hasPendingWorkLocked 执行实际计数；调用者必须持有 threadsMu。
func (p *Coroutines) hasPendingWorkLocked(skip Thread) bool {
	remaining := len(p.allThreads)
	if _, registered := p.allThreads[skip]; registered {
		remaining--
	}
	return remaining != 0 || p.workerCount != 0
}
