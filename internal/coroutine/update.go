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
	sdebug "runtime/debug"
	stime "time"

	"github.com/goplus/spx/v3/internal/log"
	itime "github.com/goplus/spx/v3/internal/time"
)

// updateState 是一次 Update 内保持不变或由该 Update 独占维护的运行快照。
type updateState struct {
	// frame 是 Update 开始时的引擎帧号。
	frame int64
	// levelTime 是 Update 开始时的关卡时钟秒数。
	levelTime float64
	// watchdogDeadline 是整次 Update 调度循环允许持续到的墙钟截止点。
	watchdogDeadline stime.Time
	// workDeadline 是普通循环允许在本帧继续开启额外脚本轮次的截止点。
	workDeadline stime.Time
}

// updateAction 表示 runUpdateLoop 下一步应执行的动作。
type updateAction uint8

const (
	// updateProcessJob 表示 currentJobs 非空，可以处理一个队头任务。
	updateProcessJob updateAction = iota
	// updateRetry 表示状态刚发生变化，需要回到循环重新检查，而不是取任务。
	updateRetry
	// updateComplete 表示当前队列为空且没有可继续推进的 runnable Thread。
	updateComplete
	// updateAwaitInitialization 表示引擎尚未初始化且当前无任务，已经短暂等待。
	updateAwaitInitialization

	// updateWatchdogTimeout 限制一次 Update 调度循环最长占用一秒；超时只把
	// 剩余工作推迟到下一帧，不会直接取消脚本。
	updateWatchdogTimeout = stime.Second
)

// Update 处理已排队 WaitJob，并恢复当前条件已满足的 Thread。
func (p *Coroutines) Update() {
	// 引擎通常每个物理帧调用一次 Update。一次 Update 内可能执行多个脚本
	// 片段，甚至在没有重绘时执行多轮 forever；Update 本身不等于一个脚本。
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

// readGCStatsBeforeUpdate 在性能调试开启时读取 Update 前的 GC 基线；关闭时
// 返回 nil，使调用方跳过有额外成本的 GC 差值统计。
func (p *Coroutines) readGCStatsBeforeUpdate() *sdebug.GCStats {
	if !p.perfDebug.Load() {
		return nil
	}
	stats := &sdebug.GCStats{}
	p.readGCStats(stats)
	return stats
}

// beginUpdate 快照帧号、关卡时钟和两个墙钟截止点，并返回初始化耗时统计。
func (p *Coroutines) beginUpdate() (UpdateJobsStats, updateState) {
	// frame 和 levelTime 在本次 Update 开始时快照，处理队列期间保持不变。
	// 所以某个脚本在本次 Update 中刚提交的时间等待，不会因为处理队列耗时
	// 而在同一次 Update 内意外到期。
	start := stime.Now()
	state := updateState{
		frame:            itime.Frame(),
		levelTime:        itime.TimeSinceLevelLoad(),
		watchdogDeadline: p.updateWatchdogNow().Add(updateWatchdogTimeout),
		workDeadline:     start.Add(loopWorkBudget),
	}
	return UpdateJobsStats{InitTime: elapsedMillis(start)}, state
}

// runUpdateLoop 驱动本次 Update 的任务分类、Thread 恢复和同帧循环轮次。
// 退出主循环后统一把未完成任务提升为下一次 Update 的 currentJobs。
func (p *Coroutines) runUpdateLoop(stats *UpdateJobsStats, state *updateState) {
	start := stime.Now()
	iterations := 0

updateLoop:
	for {
		iterations++
		switch p.nextUpdateAction(stats) {
		case updateComplete:
			// 当前待处理任务已经耗尽、也没有仍在执行的脚本。此时尝试把
			// forever/repeat 在循环边界提交的 loopJobs 开成同帧下一轮。
			// 只要本帧没有 RequestRedraw 且预算未耗尽，纯计算循环就能
			// 在一次 gco.Update 中执行多轮。
			if p.queueNextLoopRound(state) {
				continue
			}
			break updateLoop
		case updateAwaitInitialization:
			state.watchdogDeadline = p.updateWatchdogNow().Add(updateWatchdogTimeout)
			continue
		case updateProcessJob:
			p.processNextWaitJob(state, stats)
		case updateRetry:
		}

		if !p.updateWatchdogNow().Before(state.watchdogDeadline) {
			// 慢帧不一定代表脚本死循环。达到看门狗截止点时，把剩余排队工作
			// 留给下一帧，而不是取消脚本或在这里等待所有清理完成。
			log.Warn("engine update exceeded 1 second - deferring remaining work to next frame (waitMainCount=%d)", stats.WaitMainCount)
			break updateLoop
		}
	}

	stats.LoopTime = elapsedMillis(start)
	stats.LoopIterations = iterations
	p.promoteDeferredJobs(stats)
}

// nextUpdateAction 根据初始化状态、currentJobs 和 threadStates 决定主循环
// 下一步。它也通过 schedulerCond 等待正在运行的 Thread 发布 Yield/结束。
func (p *Coroutines) nextUpdateAction(stats *UpdateJobsStats) updateAction {
	if !p.hasInited.Load() {
		if p.currentJobs.Count() == 0 {
			start := stime.Now()
			itime.Sleep(0.05)
			stats.WaitTime += elapsedMillis(start)
			return updateAwaitInitialization
		}
		return updateProcessJob
	}

	start := stime.Now()
	action := updateProcessJob
	p.schedulerMu.Lock()
	// 注意：这是 #1886 修复之前的实现。只有 currentJobs 已经为空时，才检查
	// 是否还有 runnable Thread，并等待它 Yield/结束。如果 currentJobs 非空，
	// 即使刚刚 Resume 的上一个 Thread 还没获得 runMu、还没执行到下一次
	// Yield，Update 也会继续 Pop 下一个 job，再 Resume 另一个 Thread。
	//
	// 结果可能同时出现多个 runnable goroutine：调度器发出 Resume(A)、
	// Resume(B) 的顺序，并不能保证 A 会先获得 runMu。#1886 的一个核心修复
	// 就是在处理下一个普通 job 前，等待当前 runnable 脚本片段让出或结束。
	if p.currentJobs.Count() == 0 {
		if p.runnableThreadCountLocked() == 0 {
			action = updateComplete
		} else {
			p.schedulerCond.Wait()
			action = updateRetry
		}
	}
	p.schedulerMu.Unlock()
	stats.WaitTime += elapsedMillis(start)
	return action
}

// processNextWaitJob 从 currentJobs 队头取出一个任务，按类型处理并累计耗时。
func (p *Coroutines) processNextWaitJob(state *updateState, stats *UpdateJobsStats) {
	start := stime.Now()
	// 这里按 currentJobs 当时的队头顺序检查任务。这个顺序是 WaitJob 的搬运/
	// 入队顺序，不天然等于 Thread ID（脚本注册顺序）。
	job := p.currentJobs.PopFront()
	stats.TaskCounts++
	p.processWaitJob(state, stats, job)
	stats.TaskProcessing += elapsedMillis(start)
}

// processWaitJob 检查一个任务的取消、帧号和时间条件：未满足的任务进入
// deferredJobs/loopJobs，已满足的任务执行回调或恢复对应 Thread。
func (p *Coroutines) processWaitJob(state *updateState, stats *UpdateJobsStats, job *WaitJob) {
	if job.Th != nil && p.isThreadCanceled(job.Th) {
		return
	}

	switch job.Type {
	case waitTypeLoop:
		// 循环边界在后续帧已经满足时直接恢复；若仍是提交它的同一帧，先进入
		// loopJobs，等当前轮所有脚本让出后再决定是否开启同帧下一轮。
		if job.Frame < state.frame {
			p.runWaitJob(job)
		} else {
			p.loopJobs.PushBack(job)
		}
	case waitTypeFrame:
		// WaitNextFrame 的任务只有 job.Frame < 当前帧时才可恢复。
		if job.Frame >= state.frame {
			// 消息 Before 在提交帧内到达这里时先延期；帧末会把该任务搬回
			// currentJobs，供后续引擎帧再次检查。
			p.deferredJobs.PushBack(job)
		} else {
			// [消息分发 10.3] 帧号已经前进，向原消息 Thread 发送 Resume。
			// goroutine 醒来后仍需重新取得 runMu，才会从 WaitNextFrame 后继续。
			p.runWaitJob(job)
			stats.WaitFrameCount++
		}
	case waitTypeTime:
		// 时间未超过截止点就继续延期；满足时只调用 Resume，并不代表脚本已
		// 获得 runMu 或已经执行了 wait 后面的语句。
		if job.Time >= state.levelTime {
			p.deferredJobs.PushBack(job)
		} else {
			p.runWaitJob(job)
		}
	case waitTypeYield:
		// 普通 yield 没有时间门槛，队列轮到它时即可恢复。
		p.runWaitJob(job)
	case waitTypeMainThread:
		// 主线程任务不恢复 Th，而是直接执行已经封装好的 Call；Call 自己负责
		// 把结果发送给 WaitMainThread 的等待者。
		job.Call()
		stats.WaitMainCount++
	}
}

// runWaitJob 执行一个已经满足条件的任务。自定义 Call 优先；否则把关联
// Thread 标为 runnable 并发送 Resume 信号。
func (p *Coroutines) runWaitJob(job *WaitJob) {
	if job.Call != nil {
		job.Call()
	} else {
		// 这里只发出恢复许可和唤醒信号。真正继续执行发生在目标 goroutine
		// 从 Yield 返回、并重新获得 runMu 之后。
		p.markRunnableAndResume(job.Th)
	}
}

// promoteDeferredJobs 完成本次 Update 的队列收尾：先把未开启的循环任务追加
// 到延期队列，再整体移动为下一次 Update 的 currentJobs，并记录搬运耗时。
func (p *Coroutines) promoteDeferredJobs(stats *UpdateJobsStats) {
	start := stime.Now()
	// 没能在当前帧开启下一轮的 loopJobs（例如已经 RequestRedraw），先并入
	// deferredJobs，再整体放回 currentJobs，等待下一次 gco.Update 处理。
	p.deferredJobs.Move(p.loopJobs)
	// 注意：此分支尚未包含 #1886。Move 只保持两个队列当时的链表顺序，
	// 不会按 Thread ID 重排。因此下一帧的 currentJobs 顺序取决于此前的
	// WaitJob 入队、检查和分类顺序，而不是它们最初被 StartBatch 创建的顺序。
	// 后创建的 Clone 若更早执行到 wait，它的删除续段就可能排到较早注册的
	// Player 碰撞检测脚本前面。
	stats.NextCount = p.deferredJobs.Count()
	p.currentJobs.Move(p.deferredJobs)
	stats.MoveTime = elapsedMillis(start)
}

// finishUpdate 计算 GC 差值、总耗时和未归类耗时，补齐最终统计结构。
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
	stats.TimeDifference = total - accounted
}

// elapsedMillis 返回从 start 到当前时刻的墙钟毫秒数。
func elapsedMillis(start stime.Time) float64 {
	return stime.Since(start).Seconds() * 1000
}
