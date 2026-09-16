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

// loopWorkBudget 是单个引擎帧允许普通脚本循环继续开启额外轮次的墙钟预算。
// Scratch 风格调度使用默认帧间隔的 75%，30 FPS 下约为 25ms。预算只阻止
// 开启下一轮，不会在一个正在执行的脚本片段中间抢占它。
const loopWorkBudget = stime.Second * 3 / (4 * time.DefaultFPS)

// WaitNextFrame 暂停当前 Thread，直到引擎帧号真正前进。
func (p *Coroutines) WaitNextFrame() {
	// 与普通循环边界不同，它使用 waitTypeFrame，因此不会在当前引擎帧的
	// 下一脚本轮次恢复。
	// 消息分发的 Before 调用到这里时，callerThread 返回的就是当前接收者
	// Thread；后续暂停和恢复不会创建第二个消息 Thread。
	p.WaitNextFrameFor(p.callerThread())
}

// WaitNextFrameFor 是可显式指定 Thread 的底层版本。me 必须正由调用 goroutine
// 承载并处于运行状态，否则 yieldAtFrame 会 panic。
func (p *Coroutines) WaitNextFrameFor(me Thread) {
	p.yieldAtFrame(me, waitTypeFrame)
}

// YieldLoopFor 在 forever/repeat 的循环边界让出 me，直到下一次允许的脚本轮次。
func (p *Coroutines) YieldLoopFor(me Thread) {
	// forever/repeat 每轮结束会走这里。它保证让其他脚本获得机会，但不承诺
	// 一定经过一次渲染：无重绘且预算充足时，可在同帧下一轮恢复。
	p.yieldAtFrame(me, waitTypeLoop)
}

// yieldAtFrame 创建带当前帧号的帧门控 WaitJob，并原子地入队、阻塞、让出。
// kind 决定它必须等到下一物理帧，还是允许在同帧下一脚本轮次恢复。
func (p *Coroutines) yieldAtFrame(me Thread, kind int) {
	if me == nil || p.callerThread() != me {
		panic(ErrCannotYieldANonrunningThread)
	}
	// Frame 保存提交任务时的引擎帧。processWaitJob 用它区分“同帧下一轮”
	// 和“必须等到下一帧”。随后 enqueueAndYield 会原子地阻塞并入队。
	// [消息分发 10.1] WaitJob.Th 指回当前消息接收者 Thread。它只描述何时
	// 恢复原 goroutine，不保存 Before 后面的 current.Wait/task.Run 代码。
	job := p.newResumeWaitJob(me, kind)
	job.Frame = time.Frame()
	// [消息分发 10.2] 原子地发布 blocked 状态并把 job 入 currentJobs，随后
	// Yield 释放 runMu。Go 调用栈停在此处，保留完整的 StartBatch 包装函数。
	p.enqueueAndYield(me, job)
}

// queueNextLoopRound 在所有当前 runnable 脚本都已让出后，尝试在同一引擎帧
// 开启下一脚本轮次。返回 true 表示 loopJobs 已转入 currentJobs，Update
// 应继续处理；返回 false 表示这些循环只能留到后续帧。
func (p *Coroutines) queueNextLoopRound(state *updateState) bool {
	// waitTypeLoop 表示 forever/repeat 等脚本到达了一轮的边界。只有所有当前
	// 可运行脚本都已让出、本帧没有请求重绘且仍在工作预算内，才在同一帧
	// 开启下一轮；否则这些任务会在 Update 收尾时转入下一帧的队列。
	if p.loopJobs.Count() == 0 || p.redrawFrame.Load() == state.frame || !stime.Now().Before(state.workDeadline) {
		return false
	}
	for p.loopJobs.Count() > 0 {
		job := p.loopJobs.PopFront()
		// 改成无帧门槛的 waitTypeYield 后重新进入 currentJobs，所以接下来仍在
		// 同一次 Update 中恢复。这是“脚本轮次”，不是新的物理渲染帧。
		job.Type = waitTypeYield
		p.currentJobs.PushBack(job)
	}
	return true
}

// RequestRedraw 标记当前帧发生了需要呈现的视觉变化。它不会立即绘制，而是
// 阻止当前轮结束后继续开启同帧脚本轮次，让引擎有机会先完成渲染。
func (p *Coroutines) RequestRedraw() {
	// 只记录发生视觉修改的逻辑帧，不在这里直接绘制。当前脚本让出且所有
	// 可运行脚本完成本轮后，queueNextLoopRound 会读取此标记；若等于当前帧，
	// forever/repeat 的 waitTypeLoop 任务不会在本帧再次恢复。
	p.redrawFrame.Store(time.Frame())
}

// ReadScriptState 在引擎线程读取脚本共享状态，并确保读取期间没有脚本执行。
//
// 它尝试取得 runMu；若当前脚本正通过 WaitMainThread 等待引擎调用，则在等待
// runMu 期间主动执行队头的主线程任务，避免“引擎等脚本、脚本等引擎”死锁。
func (p *Coroutines) ReadScriptState(call func()) {
	for !p.runMu.TryLock() {
		// runMu 加锁失败，说明当前有脚本正在执行。优先服务主线程任务，使
		// 等待该任务的脚本能够继续并最终释放 runMu。
		if job := p.takeMainThreadJob(); job != nil {
			job.Call()
		} else {
			runtime.Gosched()
		}
	}
	defer p.runMu.Unlock()

	// 此时已经独占 runMu，没有脚本正文能并发修改状态，可以安全读取。
	call()
}

// takeMainThreadJob 仅在 currentJobs 队头是主线程任务时取出并返回它。
// 若队头是普通任务，会原样放回，避免 ReadScriptState 越过既有队列顺序。
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
