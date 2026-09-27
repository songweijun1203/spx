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

// loopWorkBudget 是一次 gco.Update 可用于同帧额外脚本轮次的墙钟时间预算。
// 默认 30 FPS 时一帧约 33.3ms，这里取其 75%，即约 25ms；剩余时间留给视觉同步、
// Godot 渲染和宿主逻辑。预算耗尽只会把续体延期到下一帧，不会停止 Forever 协程。
const loopWorkBudget = stime.Second * 3 / (4 * time.DefaultFPS)

// WaitNextFrame 将当前受管协程挂起到下一逻辑帧；非受管调用属于错误并会 panic。
// 直接调用方：engine.WaitNextFrame 及脚本控制流；总体流程调用方：用户脚本。
func (p *Coroutines) WaitNextFrame() {
	p.WaitNextFrameFor(p.callerThread())
}

// WaitNextFrameFor 将指定 Thread 挂起到下一逻辑帧。
func (p *Coroutines) WaitNextFrameFor(me Thread) {
	p.yieldAtFrame(me, waitTypeFrame)
}

// YieldLoopFor 是普通 Forever/Repeat 每轮末尾的协作式让出入口。
// 它生成 waitTypeLoop 并释放 runMu，但此刻不决定恢复帧：同帧创建的任务先被 Update
// 收入 roundJobs，等当前 script round 的所有 runnable Thread 都让出后统一裁决。
func (p *Coroutines) YieldLoopFor(me Thread) {
	p.yieldAtFrame(me, waitTypeLoop)
}

// YieldToNextRoundFor 等待下一帧，或等待由其他 loop continuation 开启的同帧脚本轮次。
// 它自身不会成为开启额外轮次的依据，主要用于消息接收者的同轮去重。
func (p *Coroutines) YieldToNextRoundFor(me Thread) {
	p.yieldAtFrame(me, waitTypeNextRound)
}

// RequestRedraw 记录发生视觉变化的逻辑帧。该标记属于整个 Coroutines，而非单个
// Forever：任一脚本请求重绘，当前 round 仍会完整结束，但本帧所有循环续体都不再
// 开启额外轮次。下一逻辑帧的 frame 已变化，旧标记自然失效。
func (p *Coroutines) RequestRedraw() {
	p.redrawFrame.Store(time.Frame())
}

// yieldAtFrame 把当前逻辑帧写入 WaitJob 后原子入队并 Yield。
// Frame 使 Update 能区分两种情况：同帧 waitTypeLoop 可以参加额外脚本轮次；上帧
// 遗留的循环续体则已满足“跨帧”条件，可在本次 Update 中直接恢复。
func (p *Coroutines) yieldAtFrame(me Thread, kind int) {
	p.enqueueAndYield(&WaitJob{Th: me, Type: kind, Frame: time.Frame()})
}

// queueNextScriptRound 只在所有 runnable 脚本均已让出后决定是否开启同帧下一轮。
// 必须同时满足以下条件才继续：
//  1. 当前逻辑帧没有任何脚本调用 RequestRedraw；
//  2. 本次 Update 尚未超过 beginUpdate 固定的 workDeadline；
//  3. roundJobs 中至少存在一个有效 waitTypeLoop 主动续体。
//
// 任一条件不满足都会返回 false；runUpdateLoop 随后结束，promoteDeferredJobs 把
// roundJobs 留给下一次引擎 Update。重绘标记不会中断当前 round，只阻止下一 round。
func (p *Coroutines) queueNextScriptRound(state *updateState) bool {
	// 视觉优先于同帧吞吐；预算则防止无视觉循环长期占住一次 Godot update 回调。
	if p.redrawFrame.Load() == state.frame ||
		!stime.Now().Before(state.workDeadline) || !p.hasLoopContinuation() {
		return false
	}
	// 同帧脚本轮次不推进 frame、DeltaTime 或关卡时间，只增加 scriptRound。
	p.scriptRound.Add(1)
	for p.roundJobs.Count() > 0 {
		job := p.roundJobs.PopFront()
		// 已通过本轮统一裁决，改成 waitTypeYield 后放回当前队列即可直接恢复；
		// passive 的 waitTypeNextRound 也会跟随有效的 loop continuation 一起恢复。
		job.Type = waitTypeYield
		p.currentJobs.PushBack(job)
	}
	return true
}

// hasLoopContinuation 判断 roundJobs 中是否存在仍有效、足以驱动额外轮次的循环任务。
// waitTypeNextRound 只能搭车，不能独自让 Update 不断开启空转轮次。
func (p *Coroutines) hasLoopContinuation() bool {
	return p.roundJobs.Any(func(job *WaitJob) bool {
		return job.Type == waitTypeLoop && !p.isThreadCanceled(job.Th)
	})
}
