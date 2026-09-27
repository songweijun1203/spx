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
	waitTypeFrame      = iota // 至少等待到下一逻辑帧。
	waitTypeTime              // 等待到指定关卡时间且至少跨过创建帧。
	waitTypeMainThread        // 需要由 Godot/引擎主线程执行的同步调用。
	waitTypeYield             // 已获准在当前 Update 中恢复的普通让出。
	waitTypeLoop              // 可以驱动同帧额外脚本轮次的循环让出。
	waitTypeNextRound         // 可跟随额外轮次，但自身不驱动轮次。
)

// WaitJob 描述由 Coroutines.Update 消费的等待任务。
// WaitForChan 不创建 WaitJob；它通过独立 worker 等待通道并直接恢复原协程。
type WaitJob struct {
	Th    Thread  // 等待该任务的协程；没有关联协程时为 nil。
	Type  int     // 调度器内部的等待类型。
	Call  func()  // 任务到期时执行的操作；非主线程任务为 nil，届时直接恢复 Th。
	Time  float64 // 时间等待任务使用的关卡时间截止点。
	Frame int64   // 按帧控制的任务创建时所在帧。
}

// threadOrder 返回稳定恢复顺序；没有关联 Thread 的任务排在最前面。
func (job *WaitJob) threadOrder() int64 {
	if job.Th == nil {
		return 0
	}
	return job.Th.resumeOrder
}

// taskResult 把外部 worker 的返回或 panic 安全传回原脚本协程。
type taskResult struct {
	panicValue any
	panicked   bool
}

// Wait 按关卡时间挂起当前协程 t 秒，并保证至少跨过当前逻辑帧。
//
// 直接调用方：engine.Wait 和脚本等待 API；总体流程调用方：用户脚本。非受管调用
// 直接返回，因为没有可交还的脚本执行权，也没有 Thread 可供 Update 恢复。
func (p *Coroutines) Wait(t float64) {
	me := p.callerThread()
	if me == nil {
		return
	}
	p.enqueueAndYield(&WaitJob{
		Th:    me,
		Type:  waitTypeTime,
		Time:  time.TimeSinceLevelLoad() + max(t, 0),
		Frame: time.Frame(),
	})
}

// WaitYield 挂起 me，直到某次 Update 处理它的 waitTypeYield 任务。
func (p *Coroutines) WaitYield(me Thread) {
	p.enqueueAndYield(&WaitJob{Th: me, Type: waitTypeYield})
}

// WaitToDo 把可能阻塞的 fn 放到独立 worker goroutine 中执行，并挂起当前协程。
// worker 完成后直接恢复原协程；这条路径不创建 WaitJob，也不依赖 Update。
// 如果调用者不是本管理器中的协程，则无需让出脚本执行权，直接同步执行 fn。
func (p *Coroutines) WaitToDo(fn func()) {
	// 通过 goroutine ID 找到当前调用者对应的受管协程。
	me := p.callerThread()
	if me == nil {
		fn()
		return
	}
	// worker 也属于本次协程生命周期：关闭 admission 或当前协程已取消时，
	// 不再接收新的外部等待任务。
	if !p.admitWorker(me) {
		panic(ErrAbortThread)
	}
	// 容量为 1，使 worker 可以先发布执行结果再唤醒原协程；即使原协程
	// 同时被取消，worker 也不会因无人及时接收结果而阻塞退出。
	results := make(chan taskResult, 1)
	// scheduler 状态用于表示该协程目前没有资格执行脚本切片。
	p.setThreadState(me, threadBlocked)
	go func() {
		defer p.finishWorker()
		var result taskResult
		returned := false
		defer func() {
			if !returned {
				// runtime.Goexit 会直接结束 worker，无法把正常结果交还原协程；
				// 此时取消原协程，避免它永久等待一个不会出现的结果。
				me.Cancel()
			}
			// 必须先发布结果再恢复协程，保证 WaitToDo 返回时结果已经可读。
			// 缓冲通道也允许已经取消的等待者直接退出，不会反向卡住 worker。
			results <- result
			p.markRunnableAndResume(me)
		}()
		// runExternalTask 捕获 fn 的 panic，并把它保存到 taskResult，稍后在
		// 原协程中重新抛出，从而保持调用方看到的错误语义。
		result = p.runExternalTask(fn)
		returned = true
	}()
	// Yield 释放 runMu，使其他脚本协程可以执行；worker 完成或当前协程
	// 被取消时，Resume/Cancel 会唤醒这里。恢复后还要重新取得 runMu。
	p.Yield(me)
	// worker 在发出恢复信号前已经写入结果，因此这里通常立即读到。
	result := <-results
	if result.panicked {
		panic(result.panicValue)
	}
}

// WaitForChan 从 ch 接收一个值。
//
// 在受管协程内调用时，它借助 WaitToDo 让 worker goroutine 阻塞在 select，
// 原协程则 Yield 并释放 runMu。通道收到值后，worker 直接调用恢复路径；
// 这里不会登记 WaitJob，因此不要求下一次 Coroutines.Update 才能唤醒。
// 唤醒只表示协程可以运行；它仍要等待 Go 调度并重新取得 runMu，所以不保证
// 在当前引擎帧内继续执行。协程取消时，Context.Done 会解除 worker 的通道等待。
//
// 在非受管协程中调用时则直接执行 `<-ch`，当前 goroutine 将同步阻塞。
func WaitForChan[T any](p *Coroutines, ch <-chan T) T {
	me := p.callerThread()
	if me == nil {
		return <-ch
	}

	var value T
	p.WaitToDo(func() {
		select {
		// 正常收到通道值后，由 WaitToDo 发布结果并恢复原协程。
		case value = <-ch:
		// 停止游戏或取消协程时退出等待；原协程会在恢复过程中以
		// ErrAbortThread 终止，不会把这里的零值作为正常事件交给调用方。
		case <-me.Context().Done():
		}
	})
	// 只有原协程正常恢复并重新取得脚本执行权后，才向调用方返回通道值。
	return value
}

// enqueueAndYield 原子发布“Thread 已阻塞”和对应 WaitJob，然后释放 runMu。
// 直接调用方：Wait、WaitYield 和 frame.go 的帧等待方法。
func (p *Coroutines) enqueueAndYield(job *WaitJob) {
	me := job.Th
	p.requireCurrent(me)
	// 原子地发布阻塞状态和对应的唤醒任务，避免 Update 漏掉该等待。
	p.schedulerMu.Lock()
	p.setThreadStateLocked(me, threadBlocked) // 先把调度器中的线程状态改为阻塞，再发出 WaitJob。
	p.currentJobs.PushBack(job)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
	p.Yield(me)
}

// enqueuePriorityJob 把主线程任务插到队首，使 Update/pumpMainThread 优先解除脚本依赖。
func (p *Coroutines) enqueuePriorityJob(job *WaitJob) {
	p.schedulerMu.Lock()
	p.currentJobs.PushFront(job)
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()
}

// runExternalTask 在当前 worker 中调用 fn；嵌套回调会保留外层的清理限制。
func (p *Coroutines) runExternalTask(fn func()) taskResult {
	id, previous := p.enterCallback(callbackExternal)
	defer p.leaveCallback(id, previous)
	return runTask(fn)
}

// runTask 捕获包括 panic(nil) 在内的异常，并编码为 taskResult 交还调用方。
func runTask(fn func()) (result taskResult) {
	// 用显式标记区分 panic(nil) 和正常返回。
	result.panicked = true
	defer func() {
		result.panicValue = recover()
	}()
	fn()
	result.panicked = false
	return result
}
