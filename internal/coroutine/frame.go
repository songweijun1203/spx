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

// loopWorkBudget 把默认帧间隔的 75% 留给同帧额外脚本轮次，余量交给渲染和宿主逻辑。
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

// YieldLoopFor 在普通循环边界让出；若本帧预算允许，可在同一引擎帧的下一脚本轮恢复。
func (p *Coroutines) YieldLoopFor(me Thread) {
	p.yieldAtFrame(me, waitTypeLoop)
}

// YieldToNextRoundFor 等待下一帧，或等待由其他 loop continuation 开启的同帧脚本轮次。
// 它自身不会成为开启额外轮次的依据，主要用于消息接收者的同轮去重。
func (p *Coroutines) YieldToNextRoundFor(me Thread) {
	p.yieldAtFrame(me, waitTypeNextRound)
}

// RequestRedraw 标记本帧视觉已变化；当前脚本轮结束后停止继续跑额外轮次，尽快交还渲染。
func (p *Coroutines) RequestRedraw() {
	p.redrawFrame.Store(time.Frame())
}

// yieldAtFrame 把帧号写入 WaitJob 后原子入队并 Yield。
func (p *Coroutines) yieldAtFrame(me Thread, kind int) {
	p.enqueueAndYield(&WaitJob{Th: me, Type: kind, Frame: time.Frame()})
}

// queueNextScriptRound 只在所有 runnable 脚本均已让出后决定是否开启同帧下一轮。
// 请求重绘、耗尽预算或不存在 waitTypeLoop 时结束本帧调度。
func (p *Coroutines) queueNextScriptRound(state *updateState) bool {
	if p.redrawFrame.Load() == state.frame ||
		!stime.Now().Before(state.workDeadline) || !p.hasLoopContinuation() {
		return false
	}
	// 脚本轮次不推进 frame 和关卡时间，只增加独立的 scriptRound。
	p.scriptRound.Add(1)
	for p.roundJobs.Count() > 0 {
		job := p.roundJobs.PopFront()
		job.Type = waitTypeYield
		p.currentJobs.PushBack(job)
	}
	return true
}

// hasLoopContinuation 判断 roundJobs 中是否存在仍有效、足以驱动额外轮次的循环任务。
func (p *Coroutines) hasLoopContinuation() bool {
	return p.roundJobs.Any(func(job *WaitJob) bool {
		return job.Type == waitTypeLoop && !p.isThreadCanceled(job.Th)
	})
}
