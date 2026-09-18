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

	"github.com/goplus/spx/v3/internal/time"
)

// loopWorkBudget 是单个引擎物理帧允许脚本开启“同帧额外轮次”的墙钟预算。
// 默认 FPS 为 30 时：
//
//	1 秒 / 30 帧 = 33.33ms 每帧
//	33.33ms * 75% = 25ms
//
// 这不是每一轮单独拥有 25ms，而是本次 Update 从 beginUpdate 开始共享一份
// 截止时间。它不包含 gco.Update 之前的 GameUpdate 或之后的渲染耗时。预算只
// 决定能不能开启下一轮，不会在脚本片段执行到一半时抢占它。
const loopWorkBudget = stime.Second * 3 / (4 * time.DefaultFPS)

// WaitNextFrame 挂起当前 Thread，直到引擎物理帧号真正前进。
// 它使用 waitTypeFrame，和 forever 的 waitTypeLoop 不同：即使没有重绘且预算
// 充足，WaitNextFrame 也不会在同一物理帧开启下一脚本轮次。
func (p *Coroutines) WaitNextFrame() {
	p.WaitNextFrameFor(p.callerThread())
}

// WaitNextFrameFor 是 WaitNextFrame 的显式 Thread 版本。
func (p *Coroutines) WaitNextFrameFor(me Thread) {
	p.yieldAtFrame(me, waitTypeFrame)
}

// YieldLoopFor 是 forever/repeat 普通循环边界使用的协作式让出。
//
// 当前循环完成后，yieldAtFrame 会创建 waitTypeLoop Job 并挂起 Thread。这个 Job
// 先进入 currentJobs，随后 processWaitJob 发现它产生于当前物理帧，就把它放入
// roundJobs。等本轮所有 runnable Thread 都让出后，queueNextScriptRound 再决定：
// 允许则转回 currentJobs 在同一物理帧恢复，否则留到下一物理帧。
//
// 完整的队列流向如下：
//
//	forever 一轮结束
//	    -> currentJobs(waitTypeLoop, Frame=当前物理帧)
//	    -> roundJobs（先等本轮所有脚本都到达边界）
//	        -> [无重绘且有预算] currentJobs(waitTypeYield) -> 本帧 Resume
//	        -> [有重绘或预算耗尽] deferredJobs -> 下帧 currentJobs -> 下帧 Resume
func (p *Coroutines) YieldLoopFor(me Thread) {
	p.yieldAtFrame(me, waitTypeLoop)
}

// YieldToNextRoundFor 等待下一物理帧，或者等待调度器批准的下一脚本轮次。
// 它也会进入 roundJobs；只有本帧确实存在可继续的 loop continuation，并且没有
// 重绘/预算阻挡时，才会与普通循环一起转回 currentJobs。
func (p *Coroutines) YieldToNextRoundFor(me Thread) {
	p.yieldAtFrame(me, waitTypeNextRound)
}

// RequestRedraw 标记当前物理帧已经产生需要呈现的视觉变化。
//
// 它不会立刻绘制，也不会中断正在执行的脚本片段。它只把 redrawFrame 设为当前
// 帧号；当 currentJobs 为空且所有 runnable Thread 都让出后，
// queueNextScriptRound 会发现 redrawFrame == state.frame，从而不再开启同帧
// 下一轮。当前轮的其他脚本仍会先获得执行机会，保证一轮内的协作语义完整。
func (p *Coroutines) RequestRedraw() {
	// 不需要在下一帧手工清零：下一帧 time.Frame() 会递增，旧值自然不再等于
	// 新的 updateState.frame。若下一帧又有视觉修改，则会写入新的帧号。
	p.redrawFrame.Store(time.Frame())
}

// yieldAtFrame 把 me 的“按帧/脚本轮次恢复”请求发布给调度器，然后挂起当前
// goroutine。kind 决定这个 Job 是必须跨物理帧（waitTypeFrame），还是允许在
// 当前物理帧的下一脚本轮次恢复（waitTypeLoop/waitTypeNextRound）。
func (p *Coroutines) yieldAtFrame(me Thread, kind int) {
	// 记录的是创建 Job 时的物理帧号。processWaitJob 用 job.Frame < state.frame
	// 判断它是否已经真正跨过一个引擎物理帧。
	job := p.newResumeJob(me, kind)
	job.Frame = time.Frame()
	// enqueueAndYield 会先原子发布 blocked + Job，再让当前 Thread 释放 runMu。
	// 所以 Update 不会看见“Thread 已阻塞但恢复任务尚未入队”的中间状态。
	p.enqueueAndYield(job)
}

// queueNextScriptRound 尝试开启当前物理帧的下一脚本轮次。
//
// 它只在 runUpdateLoop 发现“currentJobs 已空且没有 runnable Thread”时调用，
// 因此当前轮的所有脚本都已经到达 Wait/Yield/结束边界。三个条件必须同时满足：
//
//  1. 当前帧没有 RequestRedraw；否则应先把本轮视觉状态交给渲染阶段；
//  2. 当前物理帧的共享墙钟预算仍未到期；
//  3. roundJobs 中至少存在一个未取消的 waitTypeLoop Job。
//
// 这是一种“本次 Update 共享预算”，不是“每个 forever 每轮预算”。如果本轮
// 消耗了 8ms，下一轮只能使用剩余预算；不会重新获得 25ms。
func (p *Coroutines) queueNextScriptRound(state *updateState) bool {
	// 条件 1：本帧只要发生过一次需要呈现的视觉修改，就应结束额外脚本轮次，
	// 让 onUpdate 退出 gco.Update 后进入 OnEngineRender。这里比较帧号而非 bool，
	// 因而上一物理帧的重绘标记不会阻塞本帧。
	//
	// 条件 2：使用严格的 Now < workDeadline。等于截止点也算预算耗尽。这里只在
	// “准备开启下一整轮”时检查；已经开始运行的脚本片段不会被计时器抢占。
	//
	// 条件 3：必须有真正的 waitTypeLoop 继续项。waitTypeNextRound 是被动等待者，
	// 不能仅凭自己让 Update 在同一物理帧不断制造空转轮次。
	if p.redrawFrame.Load() == state.frame ||
		!stime.Now().Before(state.workDeadline) || !p.hasLoopContinuation() {
		return false
	}
	// 脚本轮次不是物理帧：这里不调用 time.Update，帧号和关卡逻辑时间都不变。
	// scriptRound 只用于标识同一物理帧内的第几轮脚本调度，例如消息广播的
	// 接收者去重会把“帧号 + scriptRound”作为一次轮次的标识。
	p.scriptRound.Add(1)
	for p.roundJobs.Count() > 0 {
		job := p.roundJobs.PopFront()
		// 当前帧产生的 waitTypeLoop/NextRound Job 已经越过循环边界，现在改成
		// 没有帧门槛的 waitTypeYield，放回 currentJobs 后即可在本次 Update
		// 继续处理并 Resume 原 Thread。
		job.Type = waitTypeYield
		p.currentJobs.PushBack(job)
	}
	// 返回 true 后 runUpdateLoop 直接 continue。下一次 nextUpdateAction 会从
	// currentJobs 取出这些任务并逐个 Resume；不是在本函数内直接执行用户代码。
	return true
}

// hasLoopContinuation 判断 roundJobs 中是否仍有可继续的普通循环任务。
// 被取消的 loop Job 不应单独促成新的同帧轮次；waitTypeNextRound 只有在同时
// 存在可继续 loop 时才会随本轮一起推进。
func (p *Coroutines) hasLoopContinuation() bool {
	return p.roundJobs.Any(func(job *WaitJob) bool {
		return job.Type == waitTypeLoop && !p.isThreadCanceled(job.Th)
	})
}
