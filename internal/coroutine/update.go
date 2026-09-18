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

type updateState struct {
	// frame 是进入本次 Coroutines.Update 前已经由引擎推进好的物理帧号。
	frame int64
	// levelTime 是同一时刻的关卡逻辑时间快照，供 waitTypeTime 判断截止条件。
	levelTime float64
	// watchdogDeadline 防止一次 Update 因异常调度持续超过 1 秒。它用于兜底，
	// 与正常 forever 同帧续跑使用的约 25ms workDeadline 是两套不同限制。
	watchdogDeadline stime.Time
	// workDeadline 是本次 Update 允许继续开启 forever/repeat 同帧轮次的墙钟截止点。
	// 它在 beginUpdate 计算一次，后续每个脚本轮次共享，不会随轮次重新延长。
	workDeadline stime.Time
}

type updateAction uint8

const (
	// updateProcessJob：currentJobs 队首现在可以处理。
	updateProcessJob updateAction = iota
	// updateRetry：Update 刚从 schedulerCond 醒来，需要从头重新读取共享状态。
	updateRetry
	// updateComplete：currentJobs 为空且没有 runnable Thread，当前脚本轮次已安静。
	updateComplete
	// updateAwaitInitialization：运行时尚未初始化，短暂等待后重新检查。
	updateAwaitInitialization

	updateWatchdogTimeout = stime.Second
)

// Update 处理已经排队的等待任务，并恢复当前已经满足条件的协程。
// 它由引擎 onUpdate 同步调用，本身不是一个受 Coroutines 管理的脚本 Thread。
// Update 可以在 schedulerCond 上睡眠，等待被恢复的脚本运行到下一处 Yield/结束；
// 脚本则通过 runMu 串行执行。两者这样接力，直到本轮所有脚本都再次安静下来。
func (p *Coroutines) Update() {
	// 这里的 start 是本次调度阶段的墙钟起点。物理帧号已经在更外层 updateTime
	// 中递增；本函数内部无论批准多少脚本轮次，都不会再次递增物理帧号。
	start := stime.Now()
	gcStatsBefore := p.readGCStatsBeforeUpdate()

	// state 中的 frame、levelTime 和两个 deadline 在整个 Update 期间保持不变。
	stats, state := p.beginUpdate()
	stats.GCStatsEnabled = gcStatsBefore != nil
	// runUpdateLoop 可能完成多次“恢复脚本 -> 等脚本让出 -> 批准下一轮”。
	p.runUpdateLoop(&stats, &state)
	// runUpdateLoop 退出前已经把本帧不能继续的任务整理到下次 Update 的
	// currentJobs；finishUpdate 这里只补齐耗时/GC 统计。
	p.finishUpdate(&stats, start, gcStatsBefore)
	p.statsMu.Lock()
	p.lastUpdateStats = stats
	p.statsMu.Unlock()
}

func (p *Coroutines) readGCStatsBeforeUpdate() *sdebug.GCStats {
	if !p.perfDebug.Load() {
		return nil
	}
	stats := &sdebug.GCStats{}
	p.readGCStats(stats)
	return stats
}

func (p *Coroutines) beginUpdate() (UpdateJobsStats, updateState) {
	// start 是操作系统墙钟时间，不是 SPX 的关卡逻辑时间。它既用于统计本次
	// beginUpdate 的初始化耗时，也作为同帧循环预算的起点。
	start := stime.Now()
	state := updateState{
		frame:            itime.Frame(),
		levelTime:        itime.TimeSinceLevelLoad(),
		watchdogDeadline: p.updateWatchdogNow().Add(updateWatchdogTimeout),
		// 默认 30 FPS 时 loopWorkBudget 约为 25ms。预算从本次 Update 开始计时，
		// 包括已经花掉的初始化和调度时间；它只限制“是否再开一轮”，不抢占
		// 当前已经取得 runMu 的脚本片段。
		workDeadline: start.Add(loopWorkBudget),
	}
	return UpdateJobsStats{InitTime: elapsedMillis(start)}, state
}

func (p *Coroutines) runUpdateLoop(stats *UpdateJobsStats, state *updateState) {
	start := stime.Now()
	iterations := 0

updateLoop:
	for {
		// 一次迭代只做一个调度动作：处理一个 Job、等待一次状态变化，或者判断
		// 一轮已经完成。它不等同于一次 forever 循环，也不等同于一个物理帧。
		iterations++
		switch p.nextUpdateAction(stats) {
		case updateComplete:
			// 当前 currentJobs 已空，且没有 runnable Thread。此时并不一定结束本次
			// Update：forever/repeat 可能刚把 waitTypeLoop Job 放进 roundJobs，
			// 需要先由 queueNextScriptRound 判断是否在同一物理帧继续。
			if p.queueNextScriptRound(state) {
				// true 表示 roundJobs 已搬回 currentJobs。continue 不会推进帧号，
				// 只是进入同一次 Update 的下一脚本轮次。
				continue
			}
			break updateLoop
		case updateAwaitInitialization:
			// 初始化等待不应消耗 watchdog 的一秒兜底窗口，因此从醒来时重置。
			state.watchdogDeadline = p.updateWatchdogNow().Add(updateWatchdogTimeout)
			continue
		case updateProcessJob:
			// 每次只弹出队首一个 Job。恢复 Thread 后，下一次 nextUpdateAction
			// 通常会等待这个 Thread 再次阻塞/结束，不会连续放飞所有脚本。
			p.processNextWaitJob(state, stats)
		case updateRetry:
			// Cond 唤醒不携带“为什么醒”的可靠信息；回到循环重新检查完整谓词。
		}

		if !p.updateWatchdogNow().Before(state.watchdogDeadline) {
			// 本帧变慢不一定意味着脚本失控。这里不取消脚本，也不等待清理，
			// 只是把尚未处理的任务留给下一次 Update。
			log.Warn("engine update exceeded 1 second - deferring remaining work to next frame (waitMainCount=%d)", stats.WaitMainCount)
			break updateLoop
		}
	}

	stats.LoopTime = elapsedMillis(start)
	stats.LoopIterations = iterations
	// 若 queueNextScriptRound 因重绘、预算或无 loop 返回 false，roundJobs 仍留着。
	// promoteDeferredJobs 会把它们安排给下一次物理帧，而不是丢弃。
	p.promoteDeferredJobs(stats)
}

func (p *Coroutines) nextUpdateAction(stats *UpdateJobsStats) updateAction {
	if !p.initialized.Load() {
		// 初始化前仍优先排空已经发布的 Job；完全无任务时才短暂 Sleep，避免空转。
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
	job, queued := p.currentJobs.PeekFront()
	// 这是“当前脚本轮次是否已经整体安静”的核心判断：
	//
	//  1. queued 表示 currentJobs 还有待处理项；
	//  2. hasRunnableThreadLocked 表示至少一个脚本已经被 Resume，正在竞争 runMu、
	//     正在执行用户代码，或尚未来得及发布下一次 blocked；
	//  3. roundJobs 故意不算 queued：它里面的任务正在等待整轮完成后的统一决策。
	//
	// 只要还有 runnable Thread，普通 Job 就不能抢在它本轮执行片段之前处理，所以
	// Update 在 schedulerCond 上等待。waitTypeMainThread 例外，因为当前脚本可能正
	// 等着 Update 代它执行这个引擎调用；若也等待脚本先阻塞，会形成互相等待。
	if (!queued || job.Type != waitTypeMainThread) && p.hasRunnableThreadLocked() {
		// Wait 原子释放 schedulerMu 并睡眠。脚本在 enqueueAndYield、结束等位置
		// 修改谓词后 Signal；Wait 醒来重新取得 schedulerMu，再返回 updateRetry。
		p.schedulerCond.Wait()
		action = updateRetry
	} else if !queued {
		// 没有 currentJobs，也没有未取消的 runnable Thread：当前脚本轮次结束。
		// runUpdateLoop 随后才会调用 queueNextScriptRound 检查能否同帧续跑。
		action = updateComplete
	}
	p.schedulerMu.Unlock()
	stats.WaitTime += elapsedMillis(start)
	return action
}

func (p *Coroutines) processNextWaitJob(state *updateState, stats *UpdateJobsStats) {
	start := stime.Now()
	// 队首顺序就是本轮的恢复候选顺序；一次仅取一个，恢复后的 Thread 会先获得
	// 完成本执行片段的机会，之后 Update 才继续处理下一项。
	job := p.currentJobs.PopFront()
	stats.TaskCounts++
	p.processWaitJob(state, stats, job)
	stats.TaskProcessing += elapsedMillis(start)
}

func (p *Coroutines) processWaitJob(state *updateState, stats *UpdateJobsStats, job *WaitJob) {
	if job.Th != nil && p.isThreadCanceled(job.Th) {
		return
	}

	switch job.Type {
	case waitTypeLoop, waitTypeNextRound:
		if job.Frame < state.frame {
			// Job 保留自更早的物理帧，例如上一帧因重绘或预算不足被延期。
			// 物理帧门槛已经满足，不再要求本帧还有额外轮次预算，直接恢复 Thread。
			p.runWaitJob(job)
		} else {
			// Job 在本物理帧的循环边界产生。不能立刻恢复，否则 forever 可能
			// 在当前 Thread 尚未让出给其他脚本前立即重入；先放入 roundJobs，
			// 等整轮结束后由 queueNextScriptRound 统一决定。
			p.roundJobs.PushBack(job)
		}
	case waitTypeFrame:
		// WaitNextFrame 必须观察到严格更大的物理帧号。同帧检查时只延期，绝不会
		// 因“没有重绘且有预算”转成 waitTypeYield。
		if job.Frame >= state.frame {
			p.deferredJobs.PushBack(job)
		} else {
			p.runWaitJob(job)
			stats.WaitFrameCount++
		}
	case waitTypeTime:
		// 时间等待同时要求至少跨过一个物理帧，并且关卡逻辑时间达到或超过截止值。
		// 任一条件未满足都延期，所以 Wait(0) 也不会在同物理帧立即返回。
		if job.Frame >= state.frame || job.Time > state.levelTime {
			p.deferredJobs.PushBack(job)
		} else {
			p.runWaitJob(job)
		}
	case waitTypeYield:
		// 无帧/时间门槛。queueNextScriptRound 正是把已批准的 loop Job 改成此类型。
		p.runWaitJob(job)
	case waitTypeMainThread:
		// 直接在调用 Coroutines.Update 的引擎线程执行，不恢复脚本 goroutine。
		job.Call()
		stats.WaitMainCount++
	}
}

func (p *Coroutines) runWaitJob(job *WaitJob) {
	if job.Call != nil {
		job.Call()
	} else {
		// 先把 Thread 放回 runnableThreads 并通知 Update，再通过该 Thread 私有的
		// suspendCond 唤醒 goroutine。Thread 醒来后还要取得 runMu 才能运行脚本。
		p.markRunnableAndResume(job.Th)
	}
}

func (p *Coroutines) promoteDeferredJobs(stats *UpdateJobsStats) {
	start := stime.Now()
	// queueNextScriptRound 未接纳的 roundJobs 在这里并入 deferredJobs。随后统一
	// 移回 currentJobs；注意“移回”不代表本帧继续处理，因为 runUpdateLoop 已退出。
	p.deferredJobs.Move(p.roundJobs)
	// 延期任务仍按脚本顺序保留，包括重新启动的事件处理函数。
	p.deferredJobs.SortStable(func(a, b *WaitJob) int {
		return cmp.Compare(a.threadOrder(), b.threadOrder())
	})
	stats.NextCount = p.deferredJobs.Count()
	// 下一次引擎 onUpdate 会先推进物理帧号，再调用 Coroutines.Update。届时这些
	// loop Job 满足 job.Frame < state.frame，于 processWaitJob 中直接 Resume。
	p.currentJobs.Move(p.deferredJobs)
	stats.MoveTime = elapsedMillis(start)
}

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

func elapsedMillis(start stime.Time) float64 {
	return stime.Since(start).Seconds() * 1000
}
