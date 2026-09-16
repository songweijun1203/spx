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

// BatchMode 表示 StartBatch 在返回调用方之前需要等待到哪个阶段。
//
// 它只影响 StartBatch 的返回时机，不改变批次内各 Thread 的创建顺序，
// 也不改变每个 Thread 以后通过 Wait/Yield 暂停和恢复的方式。
type BatchMode uint8

const (
	_ BatchMode = iota
	// BatchAsync 只保证这一批 Thread 已创建并开始按接力顺序放行，不等待
	// 处理函数完成第一次执行片段。
	BatchAsync
	// BatchWaitFirstSlice 等到每个处理函数至少运行至第一次 Yield/Wait 或结束。
	// OnStart 和 OnCloned 使用此模式：调用者不需要等待 forever 结束，但需要
	// 确认初始化语句（例如 setCostume、setGraphicEffect）已经执行。
	BatchWaitFirstSlice
	// BatchWaitDone 等待这一批处理函数全部结束，类似 broadcast and wait。
	BatchWaitDone
)

// BatchTask 描述有序协程批次中的一个任务。
//
// StartBatch 会按切片顺序为每个任务创建一个 Thread，并用 Latch 接力链
// 保证这些 Thread 的第一段脚本代码也按相同顺序开始执行。
type BatchTask struct {
	// Owner 用于命名、取消和事件归属，不负责决定 Go goroutine 的执行权。
	Owner ThreadObj
	// OnRegistered 在 Thread 已登记、用户代码尚未运行时同步调用。
	OnRegistered func(Thread)
	// Before 在该 Thread 取得 runMu 后、等待批次接力棒之前执行。
	Before func(Thread)
	// Run 是真正的事件处理函数包装。
	Run func(Thread)
}

// StartBatch 先按 tasks 的顺序注册整批 Thread，再按相同顺序允许其执行 Run。
//
// mode 决定本方法等待到“批次已放行”“全部完成首次执行片段”还是“全部永久
// 结束”后返回。BatchWaitFirstSlice 和 BatchWaitDone 会使用调度器感知的等待，
// 因此通常应由当前 Coroutines 管理的 Thread 调用。
//
// OnRegistered 在调用 StartBatch 的 goroutine 中同步执行，此时对应 Thread 已经
// 登记，但 Run 尚未通过接力 Latch；OnRegistered 不得反过来等待本批次完成，
// 否则会阻止后续任务注册并造成死锁。
func (p *Coroutines) StartBatch(tasks []BatchTask, mode BatchMode) []Thread {
	// [消息分发 7.1] 普通 Broadcast 传入 BatchAsync，BroadcastAndWait 传入
	// BatchWaitDone；其他值属于调用方错误。空批次无需创建任何 Thread。
	if mode != BatchAsync && mode != BatchWaitFirstSlice && mode != BatchWaitDone {
		panic("coroutine: invalid batch mode")
	}
	if len(tasks) == 0 {
		return nil
	}

	// [消息分发 7.2] progress 构成一条接力链：task[i] 等待 progress[i]，开始后打开
	// progress[i+1]。这样能把“这一批 Thread 的第一次执行”按 tasks 顺序
	// 串起来，而不是任由刚启动的 Go goroutine 决定先后。
	progress := newLatchSet(p, len(tasks)+1)
	// threads 与 tasks 保持相同索引；返回值也因此保持消息接收者的目标顺序。
	threads := make([]Thread, len(tasks))
	for i, task := range tasks {
		// current 是本任务开始 handler 前必须等到的接力棒；next 负责放行下一项。
		current, next := progress[i], progress[i+1]
		// [消息分发 7.3] 这个 for 循环先逐个 Create，所以 Thread ID 也按 tasks 的顺序递增。
		// 注意：此处的注册顺序不等于以后 WaitJob 的入队顺序。
		threads[i] = p.Create(task.Owner, func(thread Thread) int {
			// 无论 Before、Latch.Wait 或用户 handler 正常返回还是 panic，本任务
			// 永久退出前都会打开 next，避免接力链因单个任务异常而卡死。
			defer next.Open()
			if task.Before != nil {
				// [消息分发 10] 消息事件的 Before 可能调用 WaitNextFrame。若发生
				// Yield，本 goroutine 的调用栈停在这里，恢复后再继续 current.Wait。
				task.Before(thread)
			}
			// 没拿到本任务的接力棒时，Thread 会 Yield 并释放 runMu。
			current.Wait()
			// 先唤醒下一项，再运行当前任务看似会造成并行；实际上当前 Thread
			// 仍持有 runMu，下一项只能等待。当前 task.Run 执行到 Yield/结束并
			// 释放 runMu 后，下一项才可能真正执行。
			next.Open()
			// [消息分发 11] task.Run 来自 scriptEventDispatch.task。其调用栈为：
			// task.Run -> event.invoke -> doWhenIReceive.run -> 用户 OnMsg 函数。
			task.Run(thread)
			return 0
		})
		if task.OnRegistered != nil {
			// [消息分发 8] Create 返回时 Thread 已登记且 goroutine 已启动，但
			// progress[0] 尚未打开，所以用户 handler 不可能越过 current.Wait。
			// 消息 lifecycle 因而能在 handler 前可靠地写入 active 并取得 cleanup。
			task.OnRegistered(threads[i])
		}
	}

	// 某个 Thread 如果在取得接力棒前就被取消，relay 会替它打开下一道
	// latch，避免整批任务永久卡住。
	relayBatchProgress(threads, progress[1:])
	// [消息分发 9] 所有接收者都已创建并完成 OnRegistered 后，才打开第一个
	// latch。第一个 handler 可以继续，后续 handler 仍各自等待前一道接力棒。
	progress[0].Open()
	switch mode {
	case BatchWaitFirstSlice:
		// 最后一个任务开始时会打开最后一个 latch。等待者即使此刻被唤醒，
		// 也要等最后一个任务在第一次 Yield/结束时释放 runMu，才能继续执行。
		// 因而返回时，这批处理函数都已经完成了自己的第一次执行片段。
		progress[len(tasks)].Wait()
	case BatchWaitDone:
		// [消息分发 14] BroadcastAndWait 的发起 Thread 逐个 Join 接收者。
		// Join 内部会 Yield 释放 runMu，因此接收者可以运行；只有所有 handler
		// 永久返回（不是仅仅 Wait/Yield）后，原广播调用才继续。
		p.JoinAll(threads)
	}
	// BatchAsync 不进入 switch：完成创建和首次放行后立即把 Thread 列表返回，
	// 不保证各 handler 已经执行；它们随后自行竞争 runMu 并按接力链推进。
	return threads
}

// newLatchSet 创建指定数量、初始均为关闭状态并且属于 p 的 Latch。
// 返回值按索引与批次中的接力位置一一对应。
func newLatchSet(p *Coroutines, size int) []*Latch {
	latches := make([]*Latch, size)
	for i := range latches {
		latches[i] = p.NewLatch()
	}
	return latches
}

// relayBatchProgress 为批次接力链提供取消兜底。
//
// 正常情况下，threads[i] 开始执行时会打开 progress[i]。如果该 Thread 在
// 获得接力棒之前被取消，它将没有机会执行用户包装函数；这个辅助 goroutine
// 监听其 Context，并代为打开对应 Latch，使后续任务仍能继续。
func relayBatchProgress(threads []Thread, progress []*Latch) {
	go func() {
		for i, thread := range threads {
			select {
			case <-progress[i].Done():
			case <-thread.Context().Done():
				progress[i].Open()
			}
		}
	}()
}
