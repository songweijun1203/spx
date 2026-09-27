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
	"cmp"
	sdebug "runtime/debug"
	stime "time"

	"github.com/goplus/spx/v3/internal/log"
	itime "github.com/goplus/spx/v3/internal/time"
)

// updateState 是一次 Update 调用内固定的帧快照和两类时间预算。
type updateState struct {
	frame            int64      // 进入 Update 时的逻辑帧号。
	levelTime        float64    // 进入 Update 时的关卡时间。
	watchdogDeadline stime.Time // 整个 Update 最长一秒的保护截止点。
	workDeadline     stime.Time // 当前帧允许额外脚本轮次的较短预算。
}

// updateAction 表示主循环下一步是处理任务、等待后重试，还是结束当前 Update。
type updateAction uint8

const (
	updateProcessJob updateAction = iota
	updateRetry
	updateComplete

	updateWatchdogTimeout = stime.Second // 防止单次引擎回调长期卡在调度器中。
)

// Update 消费到期 WaitJob，恢复符合条件的 Thread，并等待它们再次让出或结束。
//
// 直接调用方：internal/engine.onUpdate 的 CoroUpdateJobs 阶段；总体流程调用方：Godot
// 每帧 update 回调。返回时 runnableThreads 中已经没有未取消 Thread，也没有当前帧
// 可处理任务；仍在等待未来帧、时间或外部通道的 Thread 可以继续处于挂起状态。
func (p *Coroutines) Update() {
	start := stime.Now()
	gcStatsBefore := p.readGCStatsBeforeUpdate()

	stats, state := p.beginUpdate()
	stats.GCStatsEnabled = gcStatsBefore != nil
	p.runUpdateLoop(&stats, &state)
	p.finishUpdate(&stats, start, gcStatsBefore)
	p.statsMu.Lock()
	p.lastUpdateStats = stats
	p.statsMu.Unlock()
}

// readGCStatsBeforeUpdate 在性能调试开启时取得 GC 基线，关闭时避免额外运行时开销。
func (p *Coroutines) readGCStatsBeforeUpdate() *sdebug.GCStats {
	if !p.perfDebug.Load() {
		return nil
	}
	stats := &sdebug.GCStats{}
	p.readGCStats(stats)
	return stats
}

// beginUpdate 固定本轮帧号、关卡时间和预算；循环过程中不会重新采样逻辑时间。
func (p *Coroutines) beginUpdate() (UpdateJobsStats, updateState) {
	start := stime.Now()
	state := updateState{
		frame:            itime.Frame(),
		levelTime:        itime.TimeSinceLevelLoad(),
		watchdogDeadline: p.updateWatchdogNow().Add(updateWatchdogTimeout),
		workDeadline:     start.Add(loopWorkBudget),
	}
	return UpdateJobsStats{InitTime: elapsedMillis(start)}, state
}

// runUpdateLoop 持续处理任务，必要时开启同帧额外脚本轮次，并在结束时归并延期任务。
func (p *Coroutines) runUpdateLoop(stats *UpdateJobsStats, state *updateState) {
	start := stime.Now()
	iterations := 0

updateLoop:
	for {
		iterations++
		switch p.nextUpdateAction(stats) {
		case updateComplete:
			if p.queueNextScriptRound(state) {
				continue
			}
			break updateLoop
		case updateProcessJob:
			p.processNextWaitJob(state, stats)
		case updateRetry:
		}

		if !p.updateWatchdogNow().Before(state.watchdogDeadline) {
			// 慢帧不等于脚本死循环：保留剩余任务给下一帧，不取消脚本，也不等待清理。
			log.Warn("engine update exceeded 1 second - deferring remaining work to next frame (waitMainCount=%d)", stats.WaitMainCount)
			break updateLoop
		}
	}

	stats.LoopTime = elapsedMillis(start)
	stats.LoopIterations = iterations
	p.promoteDeferredJobs(stats)
}

// nextUpdateAction 在 schedulerMu 下原子观察队首任务和 runnableThreads。
//
// 只要还有可运行脚本，Update 就在 schedulerCond 上等待它 Yield/结束；主线程任务例外，
// 因为脚本可能正持有 runMu 等待该任务完成，必须优先执行才能解除依赖。
func (p *Coroutines) nextUpdateAction(stats *UpdateJobsStats) updateAction {
	start := stime.Now()
	action := updateProcessJob
	p.schedulerMu.Lock()
	job, queued := p.currentJobs.PeekFront()
	// 即使脚本尚未 Yield，也要优先服务引擎主线程调用，否则双方会互相等待。
	if (!queued || job.Type != waitTypeMainThread) && p.hasRunnableThreadLocked() {
		p.schedulerCond.Wait()
		action = updateRetry
	} else if !queued {
		action = updateComplete
	}
	p.schedulerMu.Unlock()
	stats.WaitTime += elapsedMillis(start)
	return action
}

// processNextWaitJob 从本轮队列取出一个任务，更新统计并按类型判断是否到期。
func (p *Coroutines) processNextWaitJob(state *updateState, stats *UpdateJobsStats) {
	start := stime.Now()
	job := p.currentJobs.PopFront()
	stats.TaskCounts++
	p.processWaitJob(state, stats, job)
	stats.TaskProcessing += elapsedMillis(start)
}

// processWaitJob 按等待类型选择“本轮恢复”“同帧下一轮候选”或“延期到后续帧”。
func (p *Coroutines) processWaitJob(state *updateState, stats *UpdateJobsStats, job *WaitJob) {
	if job.Th != nil && p.isThreadCanceled(job.Th) {
		return
	}

	switch job.Type {
	case waitTypeLoop, waitTypeNextRound:
		// 旧帧遗留任务可直接恢复；本帧任务先进入 roundJobs，待所有 runnable 脚本让出后决定。
		if job.Frame < state.frame {
			p.runWaitJob(job)
		} else {
			p.roundJobs.PushBack(job)
		}
	case waitTypeFrame:
		// WaitNextFrame 至少跨过一个 frame，创建帧仍相同时不能恢复。
		if job.Frame >= state.frame {
			p.deferredJobs.PushBack(job)
		} else {
			p.runWaitJob(job)
			stats.WaitFrameCount++
		}
	case waitTypeTime:
		// 时间等待同时要求跨过创建帧，避免 Wait(0) 在当前脚本轮次立即重入。
		if job.Frame >= state.frame || job.Time > state.levelTime {
			p.deferredJobs.PushBack(job)
		} else {
			p.runWaitJob(job)
		}
	case waitTypeYield:
		// 纯 Yield 已经被允许进入本轮，直接恢复。
		p.runWaitJob(job)
	case waitTypeMainThread:
		// 主线程任务的 Call 内部负责把结果交还等待方；不走 Resume(Thread)。
		job.Call()
		stats.WaitMainCount++
	}
}

// runWaitJob 执行任务回调；普通等待没有 Call，直接把关联 Thread 标为 runnable 并唤醒。
func (p *Coroutines) runWaitJob(job *WaitJob) {
	if job.Call != nil {
		job.Call()
	} else {
		p.markRunnableAndResume(job.Th)
	}
}

// promoteDeferredJobs 在 Update 结束时把 roundJobs 合入后续任务，并稳定恢复脚本顺序。
//
// 事件处理器重启可继承 resumeOrder，所以这里不能简单按新 Thread ID 排序。
func (p *Coroutines) promoteDeferredJobs(stats *UpdateJobsStats) {
	start := stime.Now()
	p.deferredJobs.Move(p.roundJobs)
	// 延期等待保留脚本顺序，包括继承旧排序位置的重启事件处理器。
	p.deferredJobs.SortStable(func(a, b *WaitJob) int {
		return cmp.Compare(a.threadOrder(), b.threadOrder())
	})
	stats.NextCount = p.deferredJobs.Count()
	p.currentJobs.Move(p.deferredJobs)
	stats.MoveTime = elapsedMillis(start)
}

// finishUpdate 汇总可选 GC 差值和无法细分到调度阶段的外部耗时。
func (p *Coroutines) finishUpdate(stats *UpdateJobsStats, start stime.Time, before *sdebug.GCStats) {
	if before != nil {
		var after sdebug.GCStats
		p.readGCStats(&after)
		stats.GCCount = int(after.NumGC - before.NumGC)
		stats.GCPauses = float64(after.PauseTotal-before.PauseTotal) / float64(stime.Millisecond)
	}

	total := elapsedMillis(start)
	accounted := stats.InitTime + stats.LoopTime + stats.MoveTime
	stats.TotalTime = total
	stats.ExternalTime = total - accounted
}

// elapsedMillis 返回从 start 至今的毫秒数，供 UpdateJobsStats 使用。
func elapsedMillis(start stime.Time) float64 {
	return stime.Since(start).Seconds() * 1000
}
