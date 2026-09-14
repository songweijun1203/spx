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
	"runtime"
	stime "time"

	"github.com/goplus/spx/v3/internal/time"
)

// Scratch budgets 75% of the default stepping interval for script rounds.
const loopWorkBudget = stime.Second * 3 / (4 * time.DefaultFPS)

// WaitNextFrame suspends the current coroutine until the next frame.
func (p *Coroutines) WaitNextFrame() {
	p.WaitNextFrameFor(p.callerThread())
}

// WaitNextFrameFor suspends me until the next frame.
func (p *Coroutines) WaitNextFrameFor(me Thread) {
	p.yieldAtFrame(me, waitTypeFrame)
}

// YieldLoopFor yields until the next eligible script round.
func (p *Coroutines) YieldLoopFor(me Thread) {
	p.yieldAtFrame(me, waitTypeLoop)
}

func (p *Coroutines) yieldAtFrame(me Thread, kind int) {
	if me == nil || p.callerThread() != me {
		panic(ErrCannotYieldANonrunningThread)
	}
	job := p.newResumeWaitJob(me, kind)
	job.Frame = time.Frame()
	p.enqueueAndYield(me, job)
}

// Admit a new round only after all runnable scripts have yielded.
func (p *Coroutines) queueNextLoopRound(state *updateState) bool {
	// waitTypeLoop 表示 forever/repeat 等脚本到达了一轮的边界。只有所有当前
	// 可运行脚本都已让出、本帧没有请求重绘且仍在工作预算内，才在同一帧
	// 开启下一轮；否则这些任务会在 Update 收尾时转入下一帧的队列。
	if p.loopJobs.Count() == 0 || p.redrawFrame.Load() == state.frame || !stime.Now().Before(state.workDeadline) {
		return false
	}
	for p.loopJobs.Count() > 0 {
		job := p.loopJobs.PopFront()
		job.Type = waitTypeYield
		p.currentJobs.PushBack(job)
	}
	return true
}

// RequestRedraw ends additional script rounds after the current round finishes.
func (p *Coroutines) RequestRedraw() {
	// 只记录发生视觉修改的逻辑帧，不在这里直接绘制。当前脚本让出且所有
	// 可运行脚本完成本轮后，queueNextLoopRound 会读取此标记；若等于当前帧，
	// forever/repeat 的 waitTypeLoop 任务不会在本帧再次恢复。
	p.redrawFrame.Store(time.Frame())
}

// ReadScriptState reads state on the engine thread, excluding script execution.
func (p *Coroutines) ReadScriptState(call func()) {
	for !p.runMu.TryLock() {
		//runMu 加锁失败，说明当前有脚本正在执行，等待它执行完毕
		// Service engine calls so a waiting script can release runMu.
		if job := p.takeMainThreadJob(); job != nil {
			job.Call()
		} else {
			runtime.Gosched()
		}
	}
	defer p.runMu.Unlock()

	//此时 runMu 已经加锁，说明没有脚本正在执行，可以安全地读取脚本状态
	call()
}

func (p *Coroutines) takeMainThreadJob() *WaitJob {
	p.schedulerMu.Lock()
	defer p.schedulerMu.Unlock()
	if p.currentJobs.Count() == 0 {
		return nil
	}
	job := p.currentJobs.PopFront()
	if job.Type == waitTypeMainThread {
		return job
	}
	p.currentJobs.PushFront(job)
	return nil
}
