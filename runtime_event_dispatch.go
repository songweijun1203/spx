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

package spx

import (
	"slices"

	coreevent "github.com/goplus/spx/v3/internal/core/event"
	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine"
)

// scriptEventDispatch 描述一次脚本事件的“分发方案”。
//
// eventSink 保存长期注册的 owner、条件和 handler；本结构体保存本次事件携带的
// 数据及执行策略。task 会把二者组合成一次性 BatchTask，StartBatch 再为每个
// BatchTask 创建最终执行它的 Thread。
type scriptEventDispatch struct {
	// mode 决定事件发起方是立即继续、等待首个执行片段，还是等待全部结束。
	mode coroutine.BatchMode
	// matchData 用于按按键、消息名、克隆 owner 等条件筛选 sink。
	matchData any
	// before 在任务 Thread 取得 runMu 后、等待批次接力棒之前调用。
	before func(coroutine.Thread)
	// lifecycle 在 Thread 注册完成时调用，返回值在 task.Run 退出时负责清理。
	lifecycle func(coroutine.Thread, *eventSink) func()
	// shouldRun 在真正调用 handler 前做最后一次有效性检查；nil 表示始终执行。
	shouldRun func() bool
	// run 把统一的 eventSink.Handler 转回具体事件函数类型并传入本次事件参数。
	run func(coroutine.Thread, *eventSink)
}

// task 把一个长期注册的 sink 与本次分发方案组合成一次性 BatchTask。
// 对消息广播而言，每个匹配的 OnMsg sink 都经过这里，并最终得到独立 Thread。
func (p scriptEventDispatch) task(sink eventSink) coroutine.BatchTask {
	// [消息分发 6] cleanup 尚未生成；它要等 StartBatch 创建好具体 Thread，
	// 再由 OnRegistered 调用 lifecycle 获得。下面两个闭包共享这个变量。
	var cleanup func()
	task := coroutine.BatchTask{
		// Owner 会写入新 Thread.Obj，供命名、停止范围和事件归属使用。
		Owner: sink.Owner,
		// 对消息广播，这就是 doWhenIReceive 提供的可选跨帧 Before。
		Before: p.before,
		Run: func(thread coroutine.Thread) {
			// [消息分发 13] cleanup 覆盖用户 handler 正常返回和 panic 路径。
			// 对消息事件，它只在 active 仍指向当前 Thread 时清空该记录。
			if cleanup != nil {
				defer cleanup()
			}
			// [消息分发 12] 进入最终调用层。invoke 先检查 shouldRun，再调用
			// doWhenIReceive 填入的 run，后者最终执行 messageEventHandler.run。
			p.invoke(thread, &sink)
		},
	}
	if p.lifecycle != nil {
		// [消息分发 8] StartBatch 在 Create 返回后同步执行 OnRegistered。
		// 此时 Thread 已进入 allThreads/threadStates，但它的 goroutine 通常还在
		// 等 runMu；因此可以先安全建立 active Thread 等生命周期记录。
		task.OnRegistered = func(thread coroutine.Thread) {
			cleanup = p.lifecycle(thread, &sink)
		}
	}
	return task
}

// invoke 是 BatchTask.Run 与具体事件 run 之间的最后一层统一检查。
func (p scriptEventDispatch) invoke(thread coroutine.Thread, sink *eventSink) {
	// shouldRun 主要服务可能在排队期间失效的启动事件；消息广播通常为 nil，
	// 所以会直接进入 p.run。
	if p.shouldRun == nil || p.shouldRun() {
		p.run(thread, sink)
	}
}

// globalSinks 取得指定事件 Bucket 的稳定快照，并按当前 Scratch 目标顺序排列。
//
// 排序使用事件发生时的实时图层：前景精灵在前、背景精灵在后、舞台最后；
// 同一 owner 内仍保持 handler 的注册顺序。返回的只是 sink，不会创建 Thread。
func (p *scriptEventRegistry) globalSinks(bucket coreevent.Bucket) []eventSink {
	// [消息分发 3] BucketIReceive 快照冻结“本次广播能看到哪些订阅”。广播开始
	// 后再注册的 OnMsg 不会混入本批次。随后按当前实时图层重排目标执行顺序。
	return sinksInScratchTargetOrder(activeGame(), p.manager.Snapshot(bucket))
}

// dispatchGlobal 分发一个面向所有目标的事件 Bucket。
// 它取得并排序当前完整快照，然后交给 dispatchSinks 做条件匹配和协程批次启动。
func (p *scriptEventRegistry) dispatchGlobal(bucket coreevent.Bucket, event scriptEventDispatch) {
	// 先完成快照和 Scratch 排序，再进入统一的脚本调度边界。
	p.dispatchSinks(p.globalSinks(bucket), event)
}

// dispatchTarget 只向 owner 完全相同的 sink 分发目标事件。
//
// 它适用于点击、滑动、碰撞开始和克隆等“事件属于某个具体实例”的场景。
// owner 筛选在 event.matchData 条件匹配之前完成；筛选结果保持 manager 快照顺序。
func (p *scriptEventRegistry) dispatchTarget(bucket coreevent.Bucket, owner any, event scriptEventDispatch) {
	// OnCloned 等目标事件只选择 owner 完全相同的 sink。克隆在 Main 重新执行
	// 时会为自己登记 sink，因此这里匹配到的是克隆对象自己的处理函数。
	sinks := p.manager.Snapshot(bucket)
	owned := make([]eventSink, 0, len(sinks))
	for _, sink := range sinks {
		if sink.Owner == owner {
			owned = append(owned, sink)
		}
	}
	p.dispatchSinks(owned, event)
}

// dispatchSinks 在脚本协程调度边界内分发一组普通事件 sink。
//
// 调用者可以来自引擎 goroutine 或脚本 Thread；runScriptEventDispatch 会在需要时
// 创建并等待一个分发 Thread，确保 sink 匹配和整批 Thread 注册不会与脚本正文
// 无约束地并行。随后 dispatchScriptEventBatch 完成条件筛选并启动 BatchTask。
func (p *scriptEventRegistry) dispatchSinks(sinks []eventSink, event scriptEventDispatch) {
	// [消息分发 4] call 内部将完成 Cond 匹配和整批 Thread 注册。
	// runScriptEventDispatch 保证这段操作发生在受 runMu 约束的脚本上下文中。
	runScriptEventDispatch(func() {
		dispatchScriptEventBatch(sinks, event)
	})
}

// dispatchStartSinks 在脚本协程调度边界内分发 OnStart sink。
// 它与 dispatchSinks 的区别是转入专用 dispatchStartEventBatch，以协调执行期间
// 发生的 Stop(AllStop)、待启动 Thread 集合以及启动代次失效处理。
func (p *scriptEventRegistry) dispatchStartSinks(sinks []eventSink, event scriptEventDispatch) {
	runScriptEventDispatch(func() {
		p.dispatchStartEventBatch(sinks, event)
	})
}

// dispatchStartEventBatch 匹配 OnStart sink，并为本次启动快照建立受控批次。
//
// 每个 Thread 注册后先进入 pendingStartThreads；真正开始 task.Run 时再移除。
// 如果期间 stopAllEpoch 发生变化，该 Thread 会设置 StopAtNextYield：这样已经冻结
// 的 OnStart 快照仍能依次执行自己的首次片段，但不会在以后帧继续运行。
// 无协程管理器的降级路径则直接在当前 goroutine 中调用匹配的 handler。
func (p *scriptEventRegistry) dispatchStartEventBatch(sinks []eventSink, event scriptEventDispatch) {
	// matching 只决定哪些已登记处理函数参与本次事件；此时仍没有为它们
	// 创建 Thread。下面构造 tasks 并调用 StartBatch 后才产生 Thread ID。
	matched := matchingEventSinks(sinks, event.matchData)
	if len(matched) == 0 {
		return
	}
	if gco == nil {
		for i := range matched {
			event.invoke(nil, &matched[i])
		}
		return
	}

	baseline := p.stopAllEpoch.Load()
	tasks := make([]coroutine.BatchTask, len(matched))
	threads := make([]coroutine.Thread, 0, len(matched))
	defer func() {
		for _, thread := range threads {
			p.pendingStartThreads.Delete(thread)
		}
	}()
	for i, sink := range matched {
		task := event.task(sink)
		run := task.Run
		task.OnRegistered = func(thread coroutine.Thread) {
			p.pendingStartThreads.Store(thread, struct{}{})
			threads = append(threads, thread)
		}
		task.Run = func(thread coroutine.Thread) {
			p.pendingStartThreads.Delete(thread)
			if p.stopAllEpoch.Load() != baseline {
				gco.StopAtNextYield(thread)
			}
			run(thread)
		}
		tasks[i] = task
	}

	gco.StartBatch(tasks, event.mode)
}

// isPendingStartThread 报告 thread 是否已经注册为本轮 OnStart Thread、但尚未
// 进入其 task.Run。Stop(AllStop) 用它避免提前取消接力链后方的开始事件脚本。
func (p *scriptEventRegistry) isPendingStartThread(thread coroutine.Thread) bool {
	_, ok := p.pendingStartThreads.Load(thread)
	return ok
}

// eventBatchMode 把用户层的 wait 参数转换成协程批次返回策略。
// 普通 Broadcast 使用 BatchAsync；BroadcastAndWait 使用 BatchWaitDone。
func eventBatchMode(wait bool) coroutine.BatchMode {
	if wait {
		return coroutine.BatchWaitDone
	}
	return coroutine.BatchAsync
}

// sinksInScratchTargetOrder 按 Scratch 的目标顺序重排不可变 sink 快照：实时图层
// 中靠前的精灵先执行，舞台最后；同一 owner 内保持原注册顺序。
func sinksInScratchTargetOrder(game *Game, sinks []eventSink) []eventSink {
	// 这是“事件 handler 的排列顺序”：精灵按当前图层从前到后，舞台最后；
	// 同一个 owner 内保留调用 OnStart/OnMsg 等时的登记顺序。StartBatch 随后
	// 按这个切片创建 Thread，所以它会间接决定这一批 Thread ID 的先后。
	if len(sinks) < 2 {
		// 零个或一个 sink 不存在跨目标排序问题，直接复用快照。
		return sinks
	}
	if game == nil {
		// 没有活动 Game 时无法读取实时图层；仍复制切片，避免调用方意外追加时
		// 复用 manager 内部快照的底层数组。
		return slices.Clone(sinks)
	}

	// shapes 是当前渲染顺序。后面的逆序遍历会把前景精灵先追加到 ordered。
	shapes := game.getAllShapes()
	// 使用索引链把不可变快照按 owner 分组，而不是给每个精灵分配一个小切片。
	// 克隆很多时，这能避免 handler 尚未开始就产生大量 Web 端 GC 压力。
	// 链接索引从 1 开始，0 同时表示“该组为空/链表结束”。
	heads := make(map[*SpriteImpl]int, len(shapes))
	// 先为仍存活在场景中的精灵建立分组入口；不在 shapes 中的 owner 稍后归入
	// unknown，舞台 owner 则单独归入 stage。
	for _, shape := range shapes {
		if sprite, ok := shape.(*SpriteImpl); ok {
			heads[sprite] = 0
		}
	}
	next := make([]int, len(sinks))
	unknown, stage := 0, 0
	// 从后向前扫描 sink，并把同一 owner 的注册记录串成索引链。倒序构链使得
	// 以后沿链读取时仍是原始注册顺序，而不是倒序。
	for i := len(sinks) - 1; i >= 0; i-- {
		switch owner := sinks[i].Owner.(type) {
		case *SpriteImpl:
			if head, live := heads[owner]; live {
				next[i], heads[owner] = head, i+1
				continue
			}
		case *Game:
			if owner == game {
				next[i], stage = stage, i+1
				continue
			}
		}
		next[i], unknown = unknown, i+1
	}

	ordered := make([]eventSink, 0, len(sinks))
	// appendGroup 沿单向索引链，把某个 owner 的全部 sink 追加到结果。
	appendGroup := func(head int) {
		for head != 0 {
			ordered = append(ordered, sinks[head-1])
			head = next[head-1]
		}
	}
	// shapes 从后向前对应 Scratch 的前景到背景顺序，因而先追加最前面的精灵。
	for i := len(shapes) - 1; i >= 0; i-- {
		if sprite, ok := shapes[i].(*SpriteImpl); ok {
			appendGroup(heads[sprite])
		}
	}
	// 不属于当前活动精灵/舞台的 owner 保留在舞台之前，舞台 handler 最后执行。
	appendGroup(unknown)
	appendGroup(stage)
	return ordered
}

// matchingEventSinks 对已经确定顺序的 sink 统一执行本次事件条件匹配。
func matchingEventSinks(sinks []eventSink, matchData any) []eventSink {
	// 预分配最大容量只减少扩容，不代表所有 sink 都一定匹配。
	matched := make([]eventSink, 0, len(sinks))
	for _, sink := range sinks {
		// Cond=nil 表示无条件接收，例如 OnMsg__0；指定消息的 OnMsg__1 使用
		// MatchValue(msg)，只有 matchData 与注册消息名相等才进入本批次。
		if sink.Cond == nil || sink.Cond(matchData) {
			matched = append(matched, sink)
		}
	}
	return matched
}

// dispatchScriptEventBatch 先完成整批条件匹配，再启动任何用户 handler。
// 这样前一个 handler 对注册表或游戏状态的修改不会改变本次广播的接收者集合。
func dispatchScriptEventBatch(sinks []eventSink, event scriptEventDispatch) {
	// [消息分发 5] 到这里仍未创建接收者 Thread；只生成有序 matched 快照。
	matched := matchingEventSinks(sinks, event.matchData)
	dispatchMatchedScriptEventBatch(matched, event)
}

// dispatchMatchedScriptEventBatch 把匹配结果逐项转换为 BatchTask，并交给协程
// 管理器创建、排序和执行 Thread。该函数不会再次求值 sink.Cond。
func dispatchMatchedScriptEventBatch(matched []eventSink, event scriptEventDispatch) {
	if len(matched) == 0 {
		// 没有接收者时既不创建 Thread，也不触发任何等待。
		return
	}
	if gco == nil {
		// 无协程管理器只用于降级/测试路径：按 matched 顺序直接调用 handler，
		// 此时 thread 参数为 nil，也不存在 Wait/Yield 调度语义。
		for i := range matched {
			event.invoke(nil, &matched[i])
		}
		return
	}

	// [消息分发 6] matched[i] -> tasks[i] -> threads[i]。这里保持索引不变，保证事件匹配
	// 顺序能够传递为 StartBatch 中的 Thread 注册顺序。
	tasks := make([]coroutine.BatchTask, len(matched))
	for i, sink := range matched {
		// task 捕获当前 sink，并把消息分发方案中的 lifecycle/before/run 一并
		// 包装进去；这里只构造描述对象，还没有执行用户 handler。
		tasks[i] = event.task(sink)
	}
	// [消息分发 7] StartBatch 按 tasks 顺序创建 Thread，并用 Latch 接力保证
	// handler 的首次执行顺序。event.mode 决定当前广播调用者是否等待它们结束。
	gco.StartBatch(tasks, event.mode)
}

// runScriptEventDispatch 为外部调用者建立完整的脚本注册屏障。
func runScriptEventDispatch(call func()) {
	// 外部线程不能直接与脚本并行修改调度状态，因此先创建一个受 runMu 管理
	// 的分发 Thread，并 Join 到它完成。若本来就在脚本协程内，则直接分发。
	if gco == nil || gco.IsInCoroutine() {
		// 已在脚本 Thread 内时，调用者本来就持有 runMu，可直接构造接收者批次。
		// gco=nil 是无协程降级路径，也直接调用。
		call()
		return
	}
	// 从引擎/普通 goroutine 发起广播时，先创建一个额外的“分发 Thread”。
	// 它获得 runMu 后执行 call，完成匹配及全部接收者 Thread 的同步注册。
	thread := gco.Create(engine.GetGame(), func(coroutine.Thread) int {
		call()
		return 0
	})
	// 外部 goroutine 在这里阻塞到分发 Thread 永久结束。这只保证接收者已经按
	// mode 完成相应的启动/等待语义，不表示 BatchAsync 的接收者已执行完。
	gco.Join(thread)
}
