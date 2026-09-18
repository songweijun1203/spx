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
	"github.com/goplus/spx/v3/internal/time"
)

const (
	// waitTypeFrame 必须等物理帧号前进后才能恢复。
	waitTypeFrame = iota
	// waitTypeTime 必须同时满足跨帧和关卡逻辑时间到期才能恢复。
	waitTypeTime
	// waitTypeMainThread 要求 Update 在引擎主线程上执行 Call。
	waitTypeMainThread
	// waitTypeYield 没有帧门槛，Update 处理到它时即可恢复 Thread。
	waitTypeYield
	// waitTypeLoop 表示 Forever/Repeat 的普通循环边界；整轮结束后，如果当前
	// 物理帧没有请求重绘且共享时间预算充足，可以转成 waitTypeYield 同帧恢复。
	waitTypeLoop
	// waitTypeNextRound 跟随下一脚本轮次或下一物理帧恢复，但它本身不会单独促成
	// 新轮次；本帧必须同时存在仍然有效的 waitTypeLoop。
	waitTypeNextRound
)

// WaitJob 描述一项由 Update 消费的等待/恢复任务。
type WaitJob struct {
	Th Thread // 等待该任务的协程；某些只执行 Call 的任务可以为空。
	// Id 是当前 Coroutines 内单调递增的任务序号。
	Id int64
	// Type 是 waitTypeXxx，决定任务满足条件的时机以及处理方式。
	Type int
	// Call 是任务满足条件后执行的动作；普通脚本等待任务为 nil，此时恢复 Th。
	Call func()
	// Time 是 waitTypeTime 使用的关卡逻辑时间截止值。
	Time float64
	// Frame 是创建该任务时的物理帧号，帧门控任务据此判断是否已经跨帧。
	Frame int64
}

// threadOrder 返回延期任务恢复时使用的脚本顺序；没有 Thread 的任务排在最前。
func (job *WaitJob) threadOrder() int64 {
	if job.Th == nil {
		return 0
	}
	return job.Th.resumeOrder
}

type taskResult struct {
	panicValue any
	panicked   bool
}

// Wait 按关卡逻辑时间挂起当前协程 t 秒；即使 t 为 0，也至少跨过当前物理帧。
func (p *Coroutines) Wait(t float64) {
	me := p.callerThread()
	if me == nil {
		return
	}
	job := p.newResumeJob(me, waitTypeTime)
	job.Time = time.TimeSinceLevelLoad() + max(t, 0)
	job.Frame = time.Frame()
	p.enqueueAndYield(job)
}

// WaitYield 挂起 me，直到某次 Update 处理它的普通 yield 任务。
func (p *Coroutines) WaitYield(me Thread) {
	p.enqueueAndYield(p.newResumeJob(me, waitTypeYield))
}

// WaitToDo 在协程内调用时，把 fn 放到工作 goroutine 执行并让出当前 Thread；
// 非协程调用者则直接执行 fn。
func (p *Coroutines) WaitToDo(fn func()) {
	me := p.callerThread()
	if me == nil {
		fn()
		return
	}
	if !p.admitWorker(me) {
		panic(ErrAbortThread)
	}
	results := make(chan taskResult, 1)
	p.setThreadState(me, threadBlocked)
	go func() {
		defer p.finishWorker()
		var result taskResult
		returned := false
		defer func() {
			if !returned {
				// Goexit 会直接结束工作 goroutine，无法向原脚本返回结果。
				me.Cancel()
			}
			// 先发布结果再唤醒；带缓冲通道可让已经取消的等待者正常退出。
			results <- result
			p.markRunnableAndResume(me)
		}()
		result = p.runExternalTask(fn)
		returned = true
	}()
	p.Yield(me)
	result := <-results
	if result.panicked {
		panic(result.panicValue)
	}
}

// WaitForChan 从 ch 接收一个值。在协程内等待时会让出执行权；否则直接阻塞调用者。
func WaitForChan[T any](p *Coroutines, ch <-chan T) T {
	me := p.callerThread()
	if me == nil {
		return <-ch
	}

	var value T
	p.WaitToDo(func() {
		select {
		case value = <-ch:
		case <-me.Context().Done():
		}
	})
	// 只有脚本被正常恢复后，才把结果返回给脚本代码。
	return value
}

func (p *Coroutines) nextWaitJobID() int64 {
	return p.nextJobID.Add(1)
}

func (p *Coroutines) newResumeJob(me Thread, waitType int) *WaitJob {
	// Job ID 是本 Coroutines 内唯一且单调递增的任务创建序号。跨帧恢复顺序并不
	// 直接使用它；promoteDeferredJobs 会另按 Thread.resumeOrder 做稳定排序。
	return &WaitJob{
		Th:   me,
		Id:   p.nextWaitJobID(),
		Type: waitType,
	}
}

func (p *Coroutines) enqueueAndYield(job *WaitJob) {
	me := job.Th
	p.requireCurrent(me)
	// 在 schedulerMu 内原子地发布“Thread 已阻塞”和“对应唤醒任务已入队”。
	// 这样 Update 不会观察到任务和 Thread 状态不一致，也不会丢失唤醒。
	p.schedulerMu.Lock()
	// 从 runnableThreads 删除 me。只要还有别的 runnable Thread，Update 就仍会
	// 等待；只有所有脚本都发布 blocked/结束，才可能认定当前脚本轮次完成。
	p.setThreadStateLocked(me, threadBlocked)
	// 先入 currentJobs，而不是由脚本直接决定恢复时间。waitTypeLoop 稍后会被
	// processWaitJob 分流到 roundJobs，直到调度器统一批准下一脚本轮次。
	p.currentJobs.PushBack(job)
	// 通知可能正在 nextUpdateAction 中等待状态变化的 Update。
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
	// 真正让出当前脚本：释放 runMu，并等待 Resume 修改本 Thread 的挂起状态。
	// Yield 返回时，调用栈仍停留在原来的 forever 的 yield() 调用处；随后 Go 的
	// for 循环才进入下一次 call()。所谓“再次调度”不是新建另一个 forever Thread。
	p.Yield(me)
}

func (p *Coroutines) enqueueJob(job *WaitJob) {
	p.schedulerMu.Lock()
	p.currentJobs.PushBack(job)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

func (p *Coroutines) enqueuePriorityJob(job *WaitJob) {
	p.schedulerMu.Lock()
	p.currentJobs.PushFront(job)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// runExternalTask 在此调用 fn；嵌套调用继续沿用外层的排空保护。
func (p *Coroutines) runExternalTask(fn func()) taskResult {
	id, previous := p.enterCallback(callbackExternal)
	defer p.leaveCallback(id, previous)
	return runTask(fn)
}

func runTask(fn func()) (result taskResult) {
	// 区分 panic(nil) 和正常返回。
	result.panicked = true
	defer func() {
		result.panicValue = recover()
	}()
	fn()
	result.panicked = false
	return result
}
