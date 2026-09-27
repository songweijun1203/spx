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

package engine

import (
	stdtime "time"

	"github.com/goplus/spx/v3/internal/coroutine"
	itime "github.com/goplus/spx/v3/internal/time"
)

// runWithoutScreenRefreshBudget 是“不刷新屏幕运行”模式的单协程连续执行预算。
// 它与 coroutine.loopWorkBudget 不同：后者控制一次 gco.Update 中允许多少同帧脚本
// 轮次；本预算允许 warp 协程在循环边界不释放 runMu，超时后才强制跨到下一帧。
const runWithoutScreenRefreshBudget = 500 * stdtime.Millisecond

func IsAbortThreadError(err any) bool {
	return coroutine.IsAbortThreadError(err)
}

func Wait(secs float64) float64 {
	startTime := itime.TimeSinceLevelLoad()
	gco.Wait(secs)
	return itime.TimeSinceLevelLoad() - startTime
}

func WaitYield() {
	gco.WaitYield(gco.Current())
}

func WaitNextFrame() float64 {
	gco.WaitNextFrame()
	return itime.DeltaTime()
}

func WaitNextFrameIfNeeded() float64 {
	if ShouldWaitNextFrame() {
		return WaitNextFrame()
	}
	return itime.DeltaTime()
}

func IsRunWithoutScreenRefresh() bool {
	thread := currentThread()
	return thread != nil && thread.RunWithoutScreenRefresh()
}

func SetRunWithoutScreenRefresh(enabled bool) (previous bool) {
	if thread := currentThread(); thread != nil {
		return thread.SetRunWithoutScreenRefresh(enabled)
	}
	return false
}

func RunWithoutScreenRefresh(call func()) {
	if call == nil {
		return
	}
	previous := SetRunWithoutScreenRefresh(true)
	defer SetRunWithoutScreenRefresh(previous)
	call()
}

// RunStopThisScript consumes stop-this-script signals at a procedure boundary.
func RunStopThisScript(call func()) {
	if call == nil {
		return
	}

	panicked := true
	defer func() {
		recovered := recover()
		if !panicked {
			return
		}
		if !coroutine.IsStopThisScriptError(recovered) {
			panic(recovered)
		}
	}()
	call()
	panicked = false
}

func ShouldWaitNextFrame() bool {
	if thread := currentThread(); thread != nil {
		return thread.ShouldWaitNextFrame(runWithoutScreenRefreshBudget)
	}
	return true
}

// NewControlFlowWaiter 为 Forever/Repeat/WaitUntil 等生成代码建立“每轮末尾”的等待器。
//
// 普通受管协程：
//   - YieldLoopFor 创建 waitTypeLoop 并释放 runMu；
//   - gco.Update 等本轮所有 runnable 脚本让出后，检查本帧重绘标记和工作预算；
//   - 没请求重绘且预算尚有剩余时，在同一引擎帧开启下一 script round；
//   - 否则把循环续体保留到下一次引擎 Update。
//
// RunWithoutScreenRefresh 协程：预算内不 Yield，持续执行下一轮；独立的 500ms 预算
// 耗尽后使用 WaitNextFrameFor 强制让出。这条路径用于 Scratch 的 warp 语义。
//
// 创建 waiter 时缓存当前 Thread，避免循环的每一轮重复查找 goroutine 身份。
// 创建控制流等待器
func NewControlFlowWaiter() func() {
	co := gco
	if co == nil || !co.IsInCoroutine() {
		// 非受管调用没有 waitTypeLoop 可登记，只能沿用传统的跨帧等待策略。
		return func() {
			if ShouldWaitNextFrame() {
				WaitNextFrame()
			}
		}
	}

	thread := co.Current()
	return func() {
		if !thread.RunWithoutScreenRefresh() {
			// 这里只提交“希望继续循环”，并不直接决定同帧恢复还是跨帧恢复。
			co.YieldLoopFor(thread)
		} else if thread.ShouldWaitNextFrame(runWithoutScreenRefreshBudget) {
			// warp 模式预算耗尽后明确使用 waitTypeFrame，至少跨过一个逻辑帧。
			co.WaitNextFrameFor(thread)
		} else {
			// warp 模式预算尚有剩余，继续同帧循环。不会释放 runMu，也不会让出脚本执行权。
		}
	}
}

// RequestRedraw 把“当前逻辑帧已有可见变化”传给协程调度器。
// 它不立即调用 Godot 绘制，也不立刻打断当前脚本；当前 script round 会正常收尾。
// 当 gco.Update 准备开启额外轮次时看到该标记，便停止同帧加速，让所有 roundJobs
// 留到下一引擎帧，从而尽快进入 OnEngineRender 并提交视觉变化。
func RequestRedraw() {
	if gco != nil {
		gco.RequestRedraw()
	}
}

// WaitForChan 是 engine 层对全局协程管理器 gco 的适配入口。
// eventLoop 等运行时协程通过它等待通道，同时把具体的挂起、取消和恢复机制
// 留在 internal/coroutine 包内实现。
func WaitForChan[T any](ch <-chan T) T {
	return coroutine.WaitForChan(gco, ch)
}
