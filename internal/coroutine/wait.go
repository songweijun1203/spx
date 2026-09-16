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
	"github.com/goplus/spx/v3/internal/engine/platform"
	"github.com/goplus/spx/v3/internal/time"
)

const (
	// waitTypeFrame 明确要求越过当前引擎帧（WaitNextFrame）。
	waitTypeFrame = iota
	// waitTypeTime 等待关卡时钟超过 Time（wait n 秒；等于截止值时仍延期）。
	waitTypeTime
	// waitTypeMainThread 把必须访问引擎的 Call 插到主线程执行。
	waitTypeMainThread
	// waitTypeYield 只让出当前执行片段，下一次 Update/轮次即可恢复。
	waitTypeYield
	// waitTypeLoop 是 forever/repeat 每轮末尾的协作式让出；它可能在同一
	// 引擎帧的下一脚本轮次恢复，也可能因为重绘/预算推迟到下一帧。
	waitTypeLoop
)

// WaitJob 描述一项等待条件，以及 Update 满足条件后应执行的恢复动作。
//
// WaitJob 不是一段新的脚本，也不拥有新的 Go goroutine。它只是调度器保存的
// “何时继续某个已有 Thread”的凭据。满足条件后，runWaitJob 最终对 Th 发出
// Resume；原 Thread 从自己的 Wait/Yield 调用之后继续执行。
type WaitJob struct {
	// Th 是等待该任务的 Thread。纯回调任务允许为 nil。
	Th Thread
	// Id 是 WaitJob 在当前管理器内的创建序号；它不是 Thread 注册序号。
	Id int64
	// Type 是 waitTypeFrame/time/mainThread/yield/loop 之一。
	Type int
	// Call 是任务满足条件时直接执行的动作。普通恢复任务为 nil，此时恢复 Th；
	// 主线程任务和少数自定义任务使用 Call。
	Call func()
	// Time 是 waitTypeTime 使用的绝对关卡时间截止点，单位为秒。
	Time float64
	// Frame 是帧门控任务提交时记录的引擎帧号。
	Frame int64
}

// taskResult 保存一个任意函数的执行结果，使跨 goroutine 调用既能返回成功，
// 也能把原始 panic 值重新传播给等待方。
type taskResult struct {
	// panicValue 是 recover 得到的值；正常返回时通常为 nil。
	panicValue any
	// panicked 区分“正常返回 nil”和“panic(nil)”等情况。
	panicked bool
}

// nativeTask 表示由 WaitToDo 启动、在脚本调度器之外执行的工作。
type nativeTask struct {
	// id 是管理器内单调递增的 native task 标识。
	id uint64
}

// runTask 执行 fn 并捕获全部 panic，把控制流转换成 taskResult。
// 调用者负责在合适的 goroutine 中重新抛出 panic。
func runTask(fn func()) (result taskResult) {
	result.panicked = true
	defer func() {
		result.panicValue = recover()
	}()
	fn()
	result.panicked = false
	return result
}

// Wait 按关卡时钟暂停当前 Thread t 秒。它不会创建新 Thread，而是提交一个
// waitTypeTime 任务并在原调用栈上 Yield；恢复后从 Wait 调用之后继续。
func (p *Coroutines) Wait(t float64) {
	me := p.callerThread()
	if me == nil {
		return
	}
	// 记录绝对截止时间。Update 在一开始会快照 levelTime，本次 Update 内
	// 不再推进这个值，因此新提交的时间等待至少要留到后续 Update 再判断。
	// 30 FPS 时 wait 0.01 通常在约 0.033 秒后的下一帧已经满足。
	deadline := time.TimeSinceLevelLoad() + t
	job := p.newResumeWaitJob(me, waitTypeTime)
	job.Time = deadline
	p.enqueueAndYield(me, job)
}

// WaitMainThread 在引擎主线程执行 call。
//
// 若当前平台允许直接调用引擎，则立即执行；否则把高优先级 main-thread job
// 放到 currentJobs 队头，并等待主线程执行结果。来自受管 Thread 的调用会在
// 取消时退出，来自普通 goroutine 的调用则阻塞等待结果。
func (p *Coroutines) WaitMainThread(call func()) {
	if platform.TryCallEngineDirectly(call) {
		return
	}

	jobID := p.nextWaitJobID()
	done := make(chan taskResult, 1)
	me := p.currentCoroutineThread()
	job := &WaitJob{
		Th:   me,
		Id:   jobID,
		Type: waitTypeMainThread,
		Call: func() {
			result := taskResult{}
			defer func() { done <- result }()
			if p.isThreadCanceled(me) {
				return
			}
			result = runTask(call)
		},
	}
	p.enqueuePriorityJob(job)

	if me == nil {
		result := <-done
		if result.panicked {
			panic(result.panicValue)
		}
		return
	}
	select {
	case result := <-done:
		if result.panicked {
			panic(result.panicValue)
		}
	case <-me.Context().Done():
		panic(ErrAbortThread)
	}
}

// WaitToDo 在独立普通 goroutine 中执行 fn，并暂停当前 Thread 直到 fn 返回。
//
// 该工作登记为 nativeTask，因此 AbortAllAndWait 会等待它完成；fn 的 panic
// 会被捕获并在原脚本 Thread 恢复后重新抛出。
func (p *Coroutines) WaitToDo(fn func()) {
	me := p.callerThread()
	if me == nil {
		fn()
		return
	}
	task := p.admitNativeTask(me)
	if task == nil {
		panic(ErrAbortThread)
	}
	results := make(chan taskResult, 1)
	p.setThreadState(me, threadBlocked)
	go func() {
		defer p.finishNativeTask(task)
		result := runTask(fn)
		// 先发布结果再唤醒脚本；带缓冲 channel 允许等待者已取消时 worker 退出。
		results <- result
		p.markRunnableAndResume(me)
	}()
	p.Yield(me)
	result := <-results
	if result.panicked {
		panic(result.panicValue)
	}
}

// WaitYield 提交一个无时间/帧门槛的 waitTypeYield，并暂停 me，直到某次
// Update 处理该任务。它与 Sched 的区别是恢复必须经过 Update 任务队列。
func (p *Coroutines) WaitYield(me Thread) {
	p.enqueueAndYield(me, p.newResumeWaitJob(me, waitTypeYield))
}

// WaitForChan 从 ch 接收一个值并写入 data。受管 Thread 调用时会 Yield，另起
// goroutine 等待 channel，避免占住 runMu；普通调用者则直接阻塞接收。
func WaitForChan[T any](p *Coroutines, ch <-chan T, data *T) {
	me := p.callerThread()
	if me == nil {
		*data = <-ch
		return
	}

	p.setThreadState(me, threadBlocked)
	go func() {
		select {
		case value := <-ch:
			if p.isThreadCanceled(me) {
				return
			}
			*data = value
			p.markRunnableAndResume(me)
		case <-me.Context().Done():
		}
	}()
	p.Yield(me)
}

// nextWaitJobID 原子地分配下一个管理器内 WaitJob 序号。
func (p *Coroutines) nextWaitJobID() int64 {
	return p.nextJobID.Add(1)
}

// newResumeWaitJob 创建一个“满足条件后恢复 me”的 WaitJob。具体截止时间或
// 帧号由调用者继续填充。
func (p *Coroutines) newResumeWaitJob(me Thread, waitType int) *WaitJob {
	return &WaitJob{
		Th:   me,
		Id:   p.nextWaitJobID(),
		Type: waitType,
	}
}

// enqueueAndYield 原子发布“me 已阻塞”和“将来负责唤醒 me 的 job”，随后
// 进入 Yield。这是时间等待、帧等待和普通队列让出的公共路径。
func (p *Coroutines) enqueueAndYield(me Thread, job *WaitJob) {
	// 原子地发布阻塞状态及负责唤醒它的任务。
	//
	// 必须在同一 schedulerMu 临界区内同时完成两件事：
	//   1. 把 Thread 标为 blocked；
	//   2. 发布将来唤醒它的 WaitJob。
	// 否则 Update 可能观察到“线程已经阻塞但没有唤醒任务”，或者任务先被
	// 消费、线程随后才进入阻塞，从而丢失一次唤醒。
	p.schedulerMu.Lock()
	p.setThreadStateLocked(me, threadBlocked)
	p.currentJobs.PushBack(job)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
	// 入队只建立了恢复条件；Yield 才真正释放 runMu 并挂起当前 Go goroutine。
	p.Yield(me)
}

// enqueueJob 把普通任务追加到 currentJobs 队尾并唤醒 Update。
// 调用者不一定是受管 Thread，因此这里不执行 Yield。
func (p *Coroutines) enqueueJob(job *WaitJob) {
	p.schedulerMu.Lock()
	p.currentJobs.PushBack(job)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// enqueuePriorityJob 把任务插入 currentJobs 队头并唤醒 Update。
// 当前用于主线程调用，保证阻塞脚本所需的引擎回调能及时执行。
func (p *Coroutines) enqueuePriorityJob(job *WaitJob) {
	p.schedulerMu.Lock()
	p.currentJobs.PushFront(job)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}
