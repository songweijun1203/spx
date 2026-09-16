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

// updateState 是一次 Coroutines.Update 调用专用的调度环境快照。
//
// 它和 UpdateJobsStats 的职责不同：
//   - updateState 是调度的“输入和边界”，processWaitJob/queueNextLoopRound 会用
//     它判断任务现在能否运行，以及本次 Update 最多可以运行多久；
//   - UpdateJobsStats 是调度的“观测结果”，只累计耗时和次数，不参与决策。
//
// 除 watchdogDeadline 在初始化等待后会重置外，其余字段在一次 Update 内保持
// 不变。特别是 frame 和 levelTime 不会随着 runUpdateLoop 的实际耗时继续增长，
// 因而同一批任务都依据同一个物理帧、同一个关卡时间点接受检查。
type updateState struct {
	// frame 是 Update 开始时的引擎帧号。waitTypeFrame 和 waitTypeLoop 用
	// job.Frame 与它比较，区分任务来自当前帧还是更早的帧。
	frame int64
	// levelTime 是 Update 开始时的关卡时钟秒数。waitTypeTime 只与这个快照
	// 比较，不会因为本次调度循环自身花了几毫秒就突然在循环中途到期。
	levelTime float64
	// watchdogDeadline 是整次 Update 调度循环允许持续到的墙钟截止点。
	// 每次顶层循环结束都会检查它；超过一秒就退出，把剩余任务留到下一帧。
	watchdogDeadline stime.Time
	// workDeadline 是普通循环允许在本帧继续开启额外脚本轮次的截止点。
	// 它比 watchdogDeadline 更短，默认约 25ms，只控制是否再开启一轮
	// forever/repeat，不会抢占已经开始执行的脚本片段。
	workDeadline stime.Time
}

// updateAction 是 nextUpdateAction 交给 runUpdateLoop 的决策结果。
// nextUpdateAction 本身不负责恢复普通 WaitJob；它只观察“队列是否有任务”和
// “是否仍有 runnable Thread”，必要时等待状态变化，再告诉外层下一步做什么。
type updateAction uint8

const (
	// updateProcessJob：currentJobs 非空，外层应 PopFront 并检查一个 WaitJob。
	updateProcessJob updateAction = iota
	// updateRetry：nextUpdateAction 刚从 schedulerCond.Wait 醒来。醒来的原因可能
	// 是 Thread Yield、Thread 结束或新任务入队，所以不能沿用睡前结论，必须
	// 回到下一轮重新读取全部条件。
	updateRetry
	// updateComplete：currentJobs 为空，而且不存在无需外部条件即可推进的
	// runnable Thread。当前脚本轮次已稳定结束，外层可以考虑 loopJobs 的
	// 同帧下一轮；若不能继续，就结束本次 Update。
	updateComplete
	// updateAwaitInitialization：引擎尚未初始化且 currentJobs 为空，函数已经
	// 短暂 Sleep。外层只需刷新 watchdogDeadline 后重新检查。
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

	// beginUpdate 一次性创建两份本地值：
	//   stats：本次 Update 的性能计数器，后续各阶段通过指针向其中累加；
	//   state：本次 Update 的帧号、关卡时间和截止点快照，供调度判断使用。
	// 两者都只属于这一次 Update，不保存在 Coroutines 中供下一帧复用。
	stats, state := p.beginUpdate()
	stats.GCStatsEnabled = gcStatsBefore != nil
	// 传指针是因为 runUpdateLoop 会持续累加 stats，并可能在初始化等待后重置
	// state.watchdogDeadline；不是因为要把它们共享给脚本 goroutine。
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

// beginUpdate 为一次 Coroutines.Update 建立初始上下文。
//
// 返回值一 UpdateJobsStats 当前只填写 InitTime，其他计数器从零开始，后续由
// runUpdateLoop、promoteDeferredJobs 和 finishUpdate 逐项补齐。
// 返回值二 updateState 保存调度判断所需的稳定时间快照和两个工作截止点。
func (p *Coroutines) beginUpdate() (UpdateJobsStats, updateState) {
	// start 有两个用途：计算初始化本身的耗时，以及确定同帧循环工作预算的
	// 起点。它是操作系统墙钟，与关卡暂停/重启所影响的 levelTime 不是一类时间。
	start := stime.Now()
	state := updateState{
		// 冻结本次 Update 的逻辑帧。即使 runUpdateLoop 在同一帧执行多轮脚本，
		// state.frame 也不变，因此“同帧脚本轮次”不会伪装成新的物理帧。
		frame: itime.Frame(),
		// 冻结本次 Update 的关卡时钟。所有 wait n 秒任务在这一轮使用同一个
		// 时间点判断，避免队列靠后的任务仅因调度耗时而得到不同结果。
		levelTime: itime.TimeSinceLevelLoad(),
		// 看门狗限制整个调度循环。使用可替换的 updateWatchdogNow，便于测试
		// 精确模拟超时；它与下面普通循环的较短预算用途不同。
		watchdogDeadline: p.updateWatchdogNow().Add(updateWatchdogTimeout),
		// 同帧循环预算从 beginUpdate 的 start 计算。超过该截止点后仍允许当前
		// 脚本片段正常让出，只是不再开启下一轮 forever/repeat。
		workDeadline: start.Add(loopWorkBudget),
	}
	// 此刻只有 InitTime 非零。这里按值返回，调用者随后在自己的 stats/state
	// 局部变量上继续工作，不会与下一次 Update 共用这两个对象。
	return UpdateJobsStats{InitTime: elapsedMillis(start)}, state
}

// runUpdateLoop 驱动本次 Update 的任务分类、Thread 恢复和同帧循环轮次。
// 退出主循环后统一把未完成任务提升为下一次 Update 的 currentJobs。
//
// WaitJob 在这里的主流程为：
//
//	currentJobs --PopFront--> processWaitJob
//	     |                         |
//	     |                         +-- 条件满足 ------------> runWaitJob/Resume
//	     |                         +-- 帧/时间未满足 --------> deferredJobs
//	     |                         +-- 本帧的循环边界 ------> loopJobs
//	     |                                                       |
//	     |                    无重绘且预算足够：下一脚本轮次 ---+
//	     |<------------------------------------------------------+
//
// loopJobs 若不能在同一物理帧继续，会在 Update 收尾时先并入 deferredJobs；
// deferredJobs 随后整体搬回 currentJobs，留给下一次 gco.Update 重新检查。
func (p *Coroutines) runUpdateLoop(stats *UpdateJobsStats, state *updateState) {
	start := stime.Now()
	iterations := 0

updateLoop:
	for {
		iterations++
		// nextUpdateAction 是每轮的“门卫”：它在 schedulerMu 保护下判断现在是
		// 处理一个 Job、等待某个 runnable Thread 先让出，还是本轮已经完成。
		// 返回后才由下面的 switch 真正执行相应动作。
		switch p.nextUpdateAction(stats) {
		case updateComplete:
			// 当前待处理任务已经耗尽、也没有仍在执行的脚本。此时尝试把
			// forever/repeat 在循环边界提交的 loopJobs 开成同帧下一轮。
			// 只要本帧没有 RequestRedraw 且预算未耗尽，纯计算循环就能
			// 在一次 gco.Update 中执行多轮。
			// [WaitJob 流程 6] queueNextLoopRound 决定 loopJobs 是在当前物理帧
			// 开启下一脚本轮次，还是保留到 runUpdateLoop 退出后的帧末收尾。
			if p.queueNextLoopRound(state) {
				// 返回 true 表示 loopJobs 已经转回 currentJobs；帧号没有变化，
				// continue 只是同一次 gco.Update 中的下一轮调度。
				continue
			}
			break updateLoop
		case updateAwaitInitialization:
			// nextUpdateAction 已在初始化阶段等待了 50ms。等待本身可能超过原来
			// 的一秒看门狗期限，所以从“现在”重新给后续调度一整段看门狗时间。
			// frame、levelTime 和 workDeadline 不重取；仍属于同一次 Update。
			state.watchdogDeadline = p.updateWatchdogNow().Add(updateWatchdogTimeout)
			continue
		case updateProcessJob:
			// [WaitJob 流程 2] 队列中有可检查任务，消费一个队头 Job。
			p.processNextWaitJob(state, stats)
		case updateRetry:
			// schedulerCond 只表示“某项调度状态发生变化”，不携带变化内容。
			// 此处不直接做事，落到循环末尾检查 watchdog，再由下一轮重新判断。
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

// nextUpdateAction 根据初始化状态、currentJobs 和 threadStates 决定
// runUpdateLoop 下一步做什么。
//
// 当前 note 分支在正常阶段的判断可以概括为：
//
//	currentJobs 有任务（无论是否有 runnable）   -> updateProcessJob
//	currentJobs 为空，仍有 runnable Thread      -> Wait，醒来后 updateRetry
//	currentJobs 为空，也没有 runnable Thread    -> updateComplete
//
// runnable 不严格等于“正在执行”：新建但还在等待 runMu 的 Thread、已经收到
// Resume 但还没拿到 runMu 的 Thread，也都属于 runnable。只要存在这种 Thread，
// 调度器就不能宣布当前脚本轮次结束，因为它还可能继续执行并提交新的 WaitJob。
//
// 注意：当前 note 分支是 #1886 修复前实现。它先判断 currentJobs，只在队列为空
// 时才观察 runnable Thread；因此“队列非空 + 上一个刚 Resume 的 Thread 仍为
// runnable”时会直接处理下一个 Job。这正是跨帧恢复顺序可能失真的关键。
func (p *Coroutines) nextUpdateAction(stats *UpdateJobsStats) updateAction {
	// 初始化前采用特殊路径。此时启动流程可能还不能依靠完整的 Thread 状态变化
	// 驱动 schedulerCond，因此空队列时做一次短暂 Sleep，避免忙循环占满 CPU。
	if !p.hasInited.Load() {
		if p.currentJobs.Count() == 0 {
			start := stime.Now()
			// Sleep 完成后不推断初始化一定完成，只返回 await；外层刷新一秒
			// watchdogDeadline，再进入下一轮重新检查 hasInited 和队列。
			itime.Sleep(0.05)
			stats.WaitTime += elapsedMillis(start)
			return updateAwaitInitialization
		}
		// 初始化前只要已经有任务，就允许外层处理一个任务，避免启动任务因
		// hasInited=false 永远没有机会推进。
		return updateProcessJob
	}

	// 从这里开始是初始化完成后的正常调度。WaitTime 不只统计真正睡眠时间，
	// 也包含取得 schedulerMu 和检查条件所花的少量时间。
	start := stime.Now()
	// 默认假设可以处理 Job；下面只在队列为空时改成 complete 或 retry。
	action := updateProcessJob
	// schedulerMu 同时保护 currentJobs 的调度观察、threadStates 谓词和
	// schedulerCond.Wait。生产者也在同一把锁下“修改状态/入队 + Signal”，
	// 因而从检查条件到进入 Wait 之间不会丢失通知。
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
		// 队列空时还要检查 Thread，而不能立刻 complete。runnable Thread 可能
		// 正在执行脚本，稍后才会调用 Wait/Yield 产生新的 currentJobs。
		if p.runnableThreadCountLocked() == 0 {
			// 没有队列任务，也没有可自行推进的 Thread，当前脚本轮次稳定结束。
			action = updateComplete
		} else {
			// schedulerCond.Wait 会原子地释放 schedulerMu 并挂起当前 Update
			// goroutine。Thread 在 Yield/结束/入队时修改谓词并 Signal；本函数
			// 醒来、重新取得 schedulerMu 后返回 retry，让外层从头检查。
			// Go 的 sync.Cond.Wait 只有收到 Signal/Broadcast 才返回，不存在传统意义
			// 上的虚假唤醒；但 Signal 只表示“状态变了”，变化可能是 Thread 结束、
			// Thread 阻塞或新 Job 入队，不一定真的有一个 Job 可以立刻处理，因此
			// 仍然必须返回 retry 后重新读取谓词。
			p.schedulerCond.Wait()
			action = updateRetry
		}
	}
	// 若 currentJobs 非空，旧实现保持默认的 updateProcessJob，完全不检查
	// runnableThreadCountLocked。这意味着上一个 Resume 尚未实际获得 runMu 时，
	// 外层就可能继续 Pop/Resume 下一个 Job。
	p.schedulerMu.Unlock()
	// elapsedMillis 包括等锁、条件检查和 Cond.Wait 的时间。该统计不参与调度。
	stats.WaitTime += elapsedMillis(start)
	return action
}

// processNextWaitJob 从 currentJobs 队头取出一个任务，按类型处理并累计耗时。
func (p *Coroutines) processNextWaitJob(state *updateState, stats *UpdateJobsStats) {
	start := stime.Now()
	// 这里按 currentJobs 当时的队头顺序检查任务。这个顺序是 WaitJob 的搬运/
	// 入队顺序，不天然等于 Thread ID（脚本注册顺序）。
	// [WaitJob 流程 3] 对应流程图中的 currentJobs --PopFront--> processWaitJob。
	// 每次只取一个，处理完后回到 runUpdateLoop 再决定处理下一个，还是等待
	// 已经 Resume 的 runnable Thread 让出/结束（dev 的 #1886 在此处加强顺序）。
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
			// [WaitJob 流程 4A] 这是上一物理帧留下的循环 Job，帧号已经推进，
			// 不再参与“本帧能否多开一轮”的判断，直接进入统一恢复路径。
			p.runWaitJob(job)
		} else {
			// [WaitJob 流程 4C] Job 在当前物理帧产生。先放到 loopJobs，
			// 等 currentJobs 和 runnable 脚本耗尽后，再由 queueNextLoopRound
			// 决定同帧恢复还是推迟到下一帧。
			p.loopJobs.PushBack(job)
		}
	case waitTypeFrame:
		// WaitNextFrame 的任务只有 job.Frame < 当前帧时才可恢复。
		if job.Frame >= state.frame {
			// 消息 Before 在提交帧内到达这里时先延期；帧末会把该任务搬回
			// currentJobs，供后续引擎帧再次检查。
			// [WaitJob 流程 4B] 当前帧号还没有越过提交帧。本次 Update 不再
			// 检查这个 Job；帧末会把它搬回下一次 Update 的 currentJobs。
			p.deferredJobs.PushBack(job)
		} else {
			// [消息分发 10.3] 帧号已经前进，向原消息 Thread 发送 Resume。
			// goroutine 醒来后仍需重新取得 runMu，才会从 WaitNextFrame 后继续。
			// [WaitJob 流程 4A] 帧号已经推进，进入统一恢复路径。
			p.runWaitJob(job)
			stats.WaitFrameCount++
		}
	case waitTypeTime:
		// 时间未超过截止点就继续延期；满足时只调用 Resume，并不代表脚本已
		// 获得 runMu 或已经执行了 wait 后面的语句。
		if job.Time >= state.levelTime {
			// [WaitJob 流程 4B] Update 开始时快照的关卡时间尚未超过截止值，
			// 转入 deferredJobs，留给下一次 Update 使用新的时间快照检查。
			p.deferredJobs.PushBack(job)
		} else {
			// [WaitJob 流程 4A] 逻辑时间已经超过截止值，进入统一恢复路径。
			p.runWaitJob(job)
		}
	case waitTypeYield:
		// 普通 yield 没有时间门槛，队列轮到它时即可恢复。
		// [WaitJob 流程 4A] 因此它有可能在提交它的同一次 gco.Update、同一
		// 物理帧内恢复；它只切分脚本执行片段，不建立帧屏障。
		p.runWaitJob(job)
	case waitTypeMainThread:
		// 主线程任务不恢复 Th，而是直接执行已经封装好的 Call；Call 自己负责
		// 把结果发送给 WaitMainThread 的等待者。
		// [WaitJob 流程 4D] 这是流程图中普通 Resume 分支的例外：直接在消费
		// Job 的引擎线程执行 Call，再由 Call 把结果交给 WaitMainThread 调用者。
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
		// [WaitJob 流程 5] 条件满足后的公共唤醒点：先把 Thread 标为 runnable，
		// 再调用 Resume -> thread.suspendCond.Signal。Signal 只唤醒其 Go
		// goroutine；它仍需取得 runMu，才会从原来的 Wait/Yield 后继续执行。
		p.markRunnableAndResume(job.Th)
	}
}

// promoteDeferredJobs 完成本次 Update 的队列收尾：先把未开启的循环任务追加
// 到延期队列，再整体移动为下一次 Update 的 currentJobs，并记录搬运耗时。
func (p *Coroutines) promoteDeferredJobs(stats *UpdateJobsStats) {
	start := stime.Now()
	// 没能在当前帧开启下一轮的 loopJobs（例如已经 RequestRedraw），先并入
	// deferredJobs，再整体放回 currentJobs，等待下一次 gco.Update 处理。
	// [WaitJob 流程 7] queueNextLoopRound 返回 false 后，仍留在 loopJobs 的
	// 循环任务本帧不会恢复。先把它们追加到 deferredJobs，统一作为跨帧任务。
	p.deferredJobs.Move(p.loopJobs)
	// 注意：此分支尚未包含 #1886。Move 只保持两个队列当时的链表顺序，
	// 不会按 Thread ID 重排。因此下一帧的 currentJobs 顺序取决于此前的
	// WaitJob 入队、检查和分类顺序，而不是它们最初被 StartBatch 创建的顺序。
	// 后创建的 Clone 若更早执行到 wait，它的删除续段就可能排到较早注册的
	// Player 碰撞检测脚本前面。
	stats.NextCount = p.deferredJobs.Count()
	// [WaitJob 流程 8] 把全部延期任务搬回 currentJobs。这里只是为下一次
	// gco.Update 准备队列，不会在已经退出的本次 runUpdateLoop 中再次处理；
	// 下一物理帧 updateTime 先推进 Frame/levelTime，随后 gco.Update 重新检查。
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
