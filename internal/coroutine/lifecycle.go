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
	sdebug "runtime/debug"

	"github.com/visualfc/gid"
)

// Task 描述一个待创建 Thread 的 owner、同步登记钩子和用户执行体。
// Setup 属于注册阶段，Run 属于新 goroutine 的脚本执行阶段。
type Task struct {
	Owner ThreadObj // 用于停止范围、事件归属和诊断名称。
	// Setup 在 Thread 登记后由创建方同步执行，并可返回退出时清理函数。
	// 即使 Run 因取消从未开始，cleanup 也会执行一次。
	Setup func(Thread) (cleanup func())
	Run   func(Thread) // 获取 runMu 后执行的用户脚本入口。
}

// Create 登记并启动一个 Thread，但不会让当前调用方主动把执行权交给它。
//
// 直接调用方：engine.Go、帧回调、事件派发辅助代码等；总体流程调用方：业务脚本、
// bootstrap 和运行时事件。新 goroutine 会等待 runMu，调用方若需要等待它，应使用
// Join、JoinYieldedOrDone 或 StartBatch 的等待模式。
func (p *Coroutines) Create(obj ThreadObj, fn func(me Thread)) Thread {
	return p.createThread(p.captureAdmission(), Task{
		Owner: obj,
		Run:   fn,
	})
}

// LastThreadID 返回最近分配的管理器内 Thread ID，主要用于诊断和测试。
func (p *Coroutines) LastThreadID() int64 {
	return p.nextThreadID.Load()
}

// threadAdmission 是创建操作开始时捕获的准入令牌。
// epoch 防止创建过程中恰逢 StopAll/重载时，把旧阶段任务登记到新生命周期。
type threadAdmission struct {
	epoch   uint64
	allowed bool
}

// valid 要求创建开始时允许准入，且期间准入代际没有发生关闭/重开。
func (a threadAdmission) valid(epoch uint64) bool {
	return a.allowed && a.epoch == epoch
}

// captureAdmission 在进入可能跨锁的创建流程前快照当前准入代际。
// 调用方本身已取消时，也不允许继续派生新 Thread。
func (p *Coroutines) captureAdmission() threadAdmission {
	epoch := p.admissionEpoch.Load()
	return threadAdmission{
		epoch:   epoch,
		allowed: epoch&1 == 0 && !p.isThreadCanceled(p.callerThread()),
	}
}

// createThread 完成 Thread 的准入、登记和可选 Setup，然后异步进入 runThread。
//
// 直接调用方：Create、StartBatch/registerBatch；总体流程调用方：所有脚本/事件协程
// 创建。defer 中启动 goroutine，保证 Setup 已返回并确定 cleanup 后 Run 才可能执行。
func (p *Coroutines) createThread(admission threadAdmission, task Task) Thread {
	th := p.newThread(task.Owner)
	var cleanup func()
	// 即使被拒绝也启动 runThread；被 Cancel 的 Thread 会在那里按统一退出流程清理。
	defer func() { go p.runThread(th, task.Run, cleanup) }()

	p.admissionMu.RLock()
	rejected := !admission.valid(p.admissionEpoch.Load())
	if rejected {
		th.Cancel()
	} else {
		p.registerThread(th)
		if task.Setup != nil {
			p.pendingSetups.Add(1)
		}
	}
	p.admissionMu.RUnlock()

	// Setup 在创建方同步执行，便于批量事件在任一处理器运行前完成全部生命周期登记。
	if !rejected && task.Setup != nil {
		id, previous := p.enterCallback(callbackSetup)
		defer p.leaveCallback(id, previous)
		setupReturned := false
		defer func() {
			if !setupReturned {
				th.Cancel()
			}
			p.finishSetup()
		}()
		cleanup = task.Setup(th)
		setupReturned = true
	}
	return th
}

// runThread 是每个受管 Go goroutine 的实际入口。
//
// 直接调用方：createThread 启动的 goroutine；总体流程调用方：Thread 生命周期。
// goroutineThreads 建立“当前 Go goroutine -> Thread”映射；runMu 保证用户 Run 和
// cleanup 所处的脚本片段不会与其他脚本并发执行。
func (p *Coroutines) runThread(th Thread, fn func(Thread), cleanup func()) {
	gid := gid.Get()
	p.goroutineThreads.Store(gid, th)
	p.runMu.Lock()
	p.setCurrent(th)
	defer func() {
		p.finishThread(th, gid, recover())
	}()
	if cleanup != nil {
		defer func() {
			id, previous := p.enterCallback(callbackCleanup)
			defer p.leaveCallback(id, previous)
			cleanup()
		}()
	}

	if th.stopped.Load() {
		panic(ErrAbortThread)
	}
	fn(th)
}

// finishThread 统一发布完成信号、唤醒 Join 等待者、释放执行权并上报 panic。
//
// 直接调用方：runThread 的 defer；总体流程调用方：正常返回、Stop/Cancel 和 panic
// 三类退出路径。必须先唤醒等待者，再移除 runnable 状态，避免 Update 误判已经无工作。
func (p *Coroutines) finishThread(th Thread, gid uint64, recovered any) {
	for waiter := range th.yieldWaiters.close(th.yieldedOrDone) {
		p.markRunnableAndResume(waiter)
	}
	for waiter := range th.joinWaiters.close(nil) {
		p.markRunnableAndResume(waiter)
	}
	// 在移除目标的调度状态前先让等待者可运行，避免中间窗口让 Update 提前返回。
	th.cancelContext()
	close(th.done)
	p.removeThreadState(th)
	p.setCurrent(nil)
	// unregisterThread 放在 panic 上报之后，确保排空操作也等待 onPanic 完成。
	defer p.unregisterThread(th)
	p.runMu.Unlock()
	id, previous := p.enterCallback(callbackFinalizing)
	defer p.leaveCallback(id, previous)
	p.goroutineThreads.Delete(gid)
	p.handleThreadPanic(th, recovered)
}

// handleThreadPanic 过滤内部控制流 panic，并把真正异常交给管理器级回调。
//
// 直接调用方：finishThread；总体流程调用方：Thread 退出收尾。没有 onPanic 时重新
// panic，让配置错误或独立使用场景仍能看到异常，而不是静默吞掉。
func (p *Coroutines) handleThreadPanic(th Thread, recovered any) {
	if recovered == nil || recovered == ErrAbortThread || recovered == ErrStopThisScript {
		return
	}
	if p.onPanic != nil {
		p.onPanic(PanicReport{
			Value:         recovered,
			Name:          th.name,
			Stack:         string(sdebug.Stack()),
			CreationStack: th.stack,
		})
		return
	}
	panic(recovered)
}

// finishSetup 结束一个同步 Setup；最后一个 Setup 返回且当前不在停止阶段时重新开放准入。
func (p *Coroutines) finishSetup() {
	p.admissionMu.Lock()
	if p.pendingSetups.Add(-1) == 0 && !p.stopping {
		p.openAdmissionLocked()
	}
	p.admissionMu.Unlock()
}

// closeAdmissionLocked 将准入代际切换为奇数。调用者必须持有 admissionMu 写锁。
func (p *Coroutines) closeAdmissionLocked() {
	if !p.admissionClosed() {
		p.admissionEpoch.Add(1)
	}
}

// openAdmissionLocked 将准入代际切换为偶数。调用者必须持有 admissionMu 写锁。
func (p *Coroutines) openAdmissionLocked() {
	if p.admissionClosed() {
		p.admissionEpoch.Add(1)
	}
}

// admissionClosed 返回当前是否拒绝创建新 Thread/worker。
func (p *Coroutines) admissionClosed() bool {
	return p.admissionEpoch.Load()&1 != 0
}

// registerThread 把 Thread 加入生命周期注册表，并在其 goroutine 启动前发布 runnable。
func (p *Coroutines) registerThread(th Thread) {
	p.threadsMu.Lock()
	p.allThreads[th] = struct{}{}
	p.threadsMu.Unlock()
	// Thread goroutine 启动前，Update 必须已经能观察到这个 runnable Thread。
	p.setThreadState(th, threadRunnable)
}

// unregisterThread 从生命周期注册表删除 Thread，并通知正在等待排空的调用方。
func (p *Coroutines) unregisterThread(th Thread) {
	p.threadsMu.Lock()
	if _, registered := p.allThreads[th]; registered {
		delete(p.allThreads, th)
		p.notifyDrainLocked()
	}
	p.threadsMu.Unlock()
}

// admitWorker 在 admission 屏障内登记一个外部 worker。
// WaitToDo 用它确保停止流程不会遗漏仍在等待 channel 等外部结果的 goroutine。
func (p *Coroutines) admitWorker(me Thread) bool {
	p.admissionMu.RLock()
	defer p.admissionMu.RUnlock()
	if p.admissionClosed() || p.isThreadCanceled(me) {
		return false
	}
	p.threadsMu.Lock()
	p.workerCount++
	p.threadsMu.Unlock()
	return true
}

// finishWorker 与 WaitToDo 中每次成功的 admitWorker 成对调用；计数归零后，
// StopAll/RunAfterStopAll 等清理流程才可以确认所有外部等待都已经退出。
func (p *Coroutines) finishWorker() {
	p.threadsMu.Lock()
	p.workerCount--
	p.notifyDrainLocked()
	p.threadsMu.Unlock()
}

// notifyDrainLocked 广播一次生命周期变化。调用者必须持有 threadsMu。
// channel 采用“一次关闭后重建”模式，避免逐个管理等待者。
func (p *Coroutines) notifyDrainLocked() {
	if p.lifecycleChanged != nil {
		close(p.lifecycleChanged)
		p.lifecycleChanged = nil
	}
}

// snapshotThreads 返回当前登记 Thread 的稳定快照，使过滤和 Cancel 不在 threadsMu 内执行。
func (p *Coroutines) snapshotThreads() []Thread {
	p.threadsMu.Lock()
	threads := make([]Thread, 0, len(p.allThreads))
	for th := range p.allThreads {
		threads = append(threads, th)
	}
	p.threadsMu.Unlock()
	return threads
}
