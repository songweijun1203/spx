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
	"context"
	"reflect"
	"runtime"
	sdebug "runtime/debug"
	"sync"
	stime "time"

	"github.com/goplus/spx/v3/internal/debug"
	"github.com/visualfc/gid"
)

// threadNamer 是 ThreadObj 可选实现的命名接口。
// newThread 会优先使用它提供的名字，使调试信息和 panic 报告更易定位。
type threadNamer interface {
	// Name 返回承载该脚本 Thread 的对象名称。
	Name() string
}

// Create 创建一个协程，但不会为了让新协程立即运行而显式让出当前执行权。
//
// Create 的“创建”是为一次脚本调用创建 Thread，而不是登记 OnStart 等事件
// 处理函数。事件函数早已存在于事件表中；事件发生后，分发器才调用 Create。
func (p *Coroutines) Create(obj ThreadObj, fn func(me Thread) int) Thread {
	// 消息批次传入的 fn 是 StartBatch 构造的包装函数，不是裸用户 Handler。
	// start=false 表示 Create 本身不额外等待新 Thread 的首次执行片段。
	return p.CreateAndStart(false, obj, fn)
}

// CreateAndStart 创建一个由独立 Go goroutine 承载的脚本 Thread。
//
// start 控制是否进行“急切启动”：为 true 时，本方法会尽量保证新 Thread 在
// 返回前至少获得一次运行机会；如果调用者本身是受管理 Thread，则准确等待子
// Thread 第一次 Yield 或结束。为 false 时只完成创建和注册，何时获得 runMu
// 由之后的调度决定。
func (p *Coroutines) CreateAndStart(start bool, obj ThreadObj, fn func(me Thread) int) Thread {
	// 先取得递增 Thread ID，再把 Thread 放入生命周期表和 threadStates。
	// 因此这里的调用先后就是“Thread 注册顺序”。它与将来 Thread 在什么
	// 时刻获得 runMu、在什么时刻执行到 Wait 没有必然的相同关系。
	// [消息分发 7.4] 先读取创建准入代次并识别父 Thread。若创建过程与
	// AbortAll 重叠，或父 Thread 已取消，新 Thread 会被拒绝而不能逃过停止屏障。
	admissionEpoch := p.abortEpoch.Load()
	parent := p.currentCoroutineThread()
	// newThread 只分配控制块、唯一 ID、Context 和条件变量，不运行 fn。
	th := p.newThread(obj)
	// creationMu 读锁把“检查准入条件 + 加入注册表”组成一个不可被全局停止
	// 屏障从中间切开的过程。
	p.creationMu.RLock()
	rejected := p.stopping || admissionEpoch&1 != 0 ||
		p.abortEpoch.Load() != admissionEpoch || p.isThreadCanceled(parent)
	if rejected {
		// 被拒绝的 Thread 仍会启动 runThread，但 stopped=true 会使它在调用 fn 前
		// 抛出 ErrAbortThread，然后走统一 finishThread 收尾。
		stopThreadIfRunning(th)
	} else {
		// [消息分发 7.5] 加入 allThreads，并在 threadStates 中发布 runnable。
		p.registerThread(th)
	}
	p.creationMu.RUnlock()
	// 每个 Thread 使用独立 Go goroutine 承载。启动 goroutine 不等于脚本
	// 已经开始执行；runThread 的第一件关键操作是等待全局 runMu。
	// [消息分发 7.6] 为这个逻辑 Thread 启动唯一的 Go goroutine。fn 作为参数
	// 保存在该 goroutine 的调用栈中；threadImpl 本身不需要保存函数指针。
	go p.runThread(th, fn)

	if start {
		// 调用者属于当前管理器时，直接等待这个子 Thread 的首次执行片段，
		// 防止另一个已经让出的任务先重新取得 runMu。
		if p.hasInited.Load() {
			if caller := p.currentCoroutineThread(); caller != nil {
				p.JoinYieldedOrDone(th)
				return th
			}
		}
		runtime.Gosched()
	}
	return th
}

// LastThreadID 返回最近一次分配的 Thread ID。
// 没有创建过 Thread 时返回零；该值用于观察分配进度，不代表 Thread 仍存活。
func (p *Coroutines) LastThreadID() int64 {
	return p.nextThreadID.Load()
}

// Abort 通过抛出内部哨兵错误 ErrAbortThread，立即终止当前协程的整个调用栈。
// runThread 的统一收尾逻辑会识别该错误，不把它当作用户代码异常上报。
func (p *Coroutines) Abort() {
	panic(ErrAbortThread)
}

// AbortThisScript 通过抛出 ErrStopThisScript，请求在最近的过程或事件边界
// 终止当前脚本；该哨兵同样不会作为普通 panic 上报。
func (p *Coroutines) AbortThisScript() {
	panic(ErrStopThisScript)
}

// AbortAll 请求取消当前已经注册的全部协程。
//
// 本方法只发出取消信号，不等待各 Thread 真正退出；需要等待完整收尾时应使用
// AbortAllAndWait 或 RunAfterAbortAll。abortEpoch 的奇偶值构成创建准入屏障，
// 防止取消快照期间并发创建的 Thread 漏掉本次取消。
func (p *Coroutines) AbortAll() {
	p.creationMu.Lock()
	// 长时间存在的停止屏障已经拒绝所有新注册。此时保持奇数 epoch 不变，
	// 但仍允许重复抓取存活 Thread 快照并再次发送取消信号。
	if !p.stopping {
		p.abortEpoch.Add(1)
	}
	p.abortAllLocked()
	if !p.stopping {
		p.abortEpoch.Add(1)
	}
	p.creationMu.Unlock()
}

// abortAllLocked 对当前 Thread 快照中的每一项发送取消信号。
// 调用者必须持有 creationMu 写锁，以便该快照与新 Thread 的准入互斥。
func (p *Coroutines) abortAllLocked() {
	for _, th := range p.snapshotThreads() {
		stopThreadIfRunning(th)
	}
}

// AbortAllAndWait 取消全部已注册协程，并等待除调用者自身以外的 Thread 停止。
//
// timeout 小于等于零表示无限等待。若由受管理 Thread 调用，等待期间会临时释放
// runMu，使其他已取消 Thread 能运行收尾代码；由普通 goroutine 调用时则转交给
// RunAfterAbortAll 建立完整的停止屏障。
func (p *Coroutines) AbortAllAndWait(timeout stime.Duration) bool {
	caller := p.currentCoroutineThread()
	if caller != nil {
		p.AbortAll()
		return p.waitForThreadsToStopFromCoroutine(timeout, caller)
	}
	return p.RunAfterAbortAll(timeout, nil)
}

// RunAfterAbortAll 在关闭新 Thread 准入后取消并排空已注册任务，再执行 call。
//
// 本方法只能从不受当前管理器控制的普通 goroutine 调用，也不能从协程的 panic
// 回调中调用。timeout 小于等于零表示无限等待。若等待超时，准入屏障会继续保持
// 关闭，直到后续调用成功完成；call 在屏障关闭期间执行，因此不得创建协程。
// 返回 true 表示所有任务已退出且 call 已执行，false 表示超时或仍有任务残留。
func (p *Coroutines) RunAfterAbortAll(timeout stime.Duration, call func()) bool {
	if p.currentCoroutineThread() != nil || p.isFinalizingCaller() {
		panic("coroutine: RunAfterAbortAll called from a managed coroutine or its panic handler")
	}
	p.shutdownMu.Lock()
	defer p.shutdownMu.Unlock()

	p.creationMu.Lock()
	p.beginStoppingLocked()
	p.creationMu.Unlock()
	completed := p.waitForThreadsToStop(timeout, nil)

	p.creationMu.Lock()
	defer p.creationMu.Unlock()
	if !completed || p.hasThreadsOtherThan(nil) {
		// 超时后的屏障保持关闭，必须通过后续成功调用显式恢复准入。
		return false
	}
	defer p.endStoppingLocked()
	if call != nil {
		call()
	}
	return true
}

// beginStoppingLocked 进入全局停止阶段并取消当前任务快照。
// 调用者必须持有 creationMu 写锁；奇数 abortEpoch 表示禁止新任务准入。
func (p *Coroutines) beginStoppingLocked() {
	if !p.stopping {
		p.stopping = true
		p.abortEpoch.Add(1)
	}
	p.abortAllLocked()
}

// endStoppingLocked 结束全局停止阶段并重新开放任务准入。
// 调用者必须持有 creationMu 写锁；递增 epoch 会将其恢复为偶数。
func (p *Coroutines) endStoppingLocked() {
	if !p.stopping {
		return
	}
	p.abortEpoch.Add(1)
	p.stopping = false
}

// StopIf 请求取消所有满足 filter 的已注册 Thread。
//
// filter 在 Thread 注册表锁之外执行，因而可以检查对象状态而不会长时间阻塞
// 注册和退出；代价是它处理的是调用瞬间取得的快照，不包含之后新建的 Thread。
func (p *Coroutines) StopIf(filter func(th Thread) bool) {
	allThreads := p.snapshotThreads()
	threads := allThreads[:0]
	for _, th := range allThreads {
		if filter(th) {
			threads = append(threads, th)
		}
	}
	for _, th := range threads {
		stopThread(th)
	}
}

// Stop 请求取消指定 Thread；传入 nil 或重复调用都是安全的。
// 取消只设置状态、关闭 Context 并唤醒可能挂起的 Thread，永久退出仍由该 Thread
// 自己在取消检查点进入 finishThread 完成。
func (p *Coroutines) Stop(thread Thread) {
	if thread != nil {
		stopThreadIfRunning(thread)
	}
}

// IsInCoroutine 报告当前调用它的 Go goroutine 是否正承载 p 管理的 Thread。
// 它检查 goroutine 到 Thread 的身份映射，而不是全局 current 调度指针。
func (p *Coroutines) IsInCoroutine() bool {
	return p.callerThread() != nil
}

// stopThreadIfRunning 在 suspendMu 保护下检查停止状态，仅对仍运行的 Thread
// 执行一次取消；它适用于允许 nil 之外重复停止的常规路径。
func stopThreadIfRunning(th Thread) {
	th.suspendMu.Lock()
	if th.stopped.Load() {
		th.suspendMu.Unlock()
		return
	}
	stopThreadLocked(th)
	th.suspendMu.Unlock()
}

// stopThread 不预先检查 stopped，直接在 suspendMu 下重复发布停止信号。
// StopIf 使用它处理快照中的目标；Cancel 和 Signal 本身允许重复调用。
func stopThread(th Thread) {
	th.suspendMu.Lock()
	stopThreadLocked(th)
	th.suspendMu.Unlock()
}

// stopThreadLocked 设置 Thread 的永久停止标志、取消 Context，并唤醒可能阻塞在
// suspendCond 上的 Resume/Yield 协作。调用者必须持有 th.suspendMu。
func stopThreadLocked(th Thread) {
	th.stopped.Store(true)
	th.Cancel()
	th.suspendCond.Signal()
}

// currentCoroutineThread 返回当前调用 goroutine 所承载的 Thread。
// 该包装保留了 Join、Latch 等同步原语使用的语义名称。
func (p *Coroutines) currentCoroutineThread() Thread {
	return p.callerThread()
}

// callerThread 返回当前调用 goroutine 所承载的协程。
//
// 这里有意不使用 Current：Current 描述的是管理器此刻记录的调度状态，不是调用
// 者身份；发生跨 goroutine 查询时，它可能指向另一个 goroutine 承载的 Thread。
func (p *Coroutines) callerThread() Thread {
	value, ok := p.goroutineThreads.Load(gid.Get())
	if !ok {
		return nil
	}
	return value.(Thread)
}

// isFinalizingCaller 报告当前 goroutine 是否正在某个 Thread 的退出/panic 回调阶段。
// 该阶段已经移除常规 Thread 身份，但仍禁止调用会等待整个管理器的关闭操作。
func (p *Coroutines) isFinalizingCaller() bool {
	_, ok := p.finalizingGoroutines.Load(gid.Get())
	return ok
}

// newThread 分配并初始化 Thread，但不把它放入管理器注册表，也不启动 goroutine。
//
// Thread ID 在这里单调递增分配；schedFrame 初始为 -1，表示尚未参加任何帧调度。
// 调试模式下还会记录创建栈，供未来的 panic 报告同时展示“在哪里创建”。
func (p *Coroutines) newThread(obj ThreadObj) Thread {
	// [消息分发 7.4a] threadImpl 是协程控制块：保存 owner、ID、取消信号、
	// Yield/Resume 条件变量和 Join 通道，但不保存要执行的 fn。
	th := &threadImpl{
		// 消息 sink.Owner 传到这里，之后 StopIf 可按对象归属筛选脚本。
		Obj: obj,
		// ID 在当前 Coroutines 内单调递增，因此 StartBatch 的 for 顺序会映射为
		// 本批接收者的 Thread ID 顺序。
		id:         p.nextThreadID.Add(1),
		schedFrame: -1,
		name:       resolveThreadName(obj),
		// done 只在永久退出时关闭，供 Join/BroadcastAndWait 等待完整 handler。
		done: make(chan struct{}),
		// yieldedOrDone 在首次 Yield 或永久退出时关闭，服务“只等首段”的事件。
		yieldedOrDone: make(chan struct{}),
	}
	// Context 负责把停止信号传播给异步等待；cancelFunc 由 Stop/finishThread 调用。
	th.ctx, th.cancelFunc = context.WithCancel(context.Background())
	if p.debug {
		th.stack = debug.GetStackTrace()
	}
	// suspendCond 是这个 Thread 自己的 goroutine 在 Yield 时睡眠、Resume 时被
	// 唤醒的条件变量；它和全局 runMu 承担不同职责。
	th.suspendCond = sync.NewCond(&th.suspendMu)
	return th
}

// resolveThreadName 从 ThreadObj 推导调试名称。
//
// 字符串对象直接作为名字；实现 threadNamer 的对象使用 Name；普通具名指针类型
// 使用“*类型名”；nil、非指针或指向匿名类型的对象返回空字符串。
func resolveThreadName(obj ThreadObj) string {
	if obj == nil {
		return ""
	}
	if name, ok := obj.(string); ok {
		return name
	}
	if named, ok := obj.(threadNamer); ok {
		return named.Name()
	}

	typ := reflect.TypeOf(obj)
	if typ.Kind() != reflect.Pointer || typ.Elem().Name() == "" {
		return ""
	}
	return "*" + typ.Elem().Name()
}

// registerThread 把 th 加入存活 Thread 表，并在调度状态表中标记为 runnable。
// 两张表使用不同的锁：前者管理生命周期，后者服务 Update 的调度等待条件。
func (p *Coroutines) registerThread(th Thread) {
	// [消息分发 7.5a] allThreads 是生命周期集合：从这里加入，到 finishThread
	// 最后 unregister 为止，AbortAll/StopIf/Join 相关逻辑都能找到该 Thread。
	p.threadsMu.Lock()
	p.allThreads[th] = struct{}{}
	p.threadsMu.Unlock()
	// [消息分发 7.5b] threadStates 是调度集合。新 Thread 尚未等待任何条件，
	// 所以先发布 runnable；这不代表它已获得 runMu 或已经执行用户代码。
	// Update 把同步完成的注册视为一道“新任务已出生”的调度屏障。
	p.setThreadState(th, threadRunnable)
}

// unregisterThread 从存活 Thread 表移除 th。
// 它在 finishThread 的最后阶段执行，表示生命周期收尾已经完成。
func (p *Coroutines) unregisterThread(th Thread) {
	p.threadsMu.Lock()
	delete(p.allThreads, th)
	p.threadsMu.Unlock()
}

// admitNativeTask 在任务准入屏障保护下登记一个 WaitToDo 原生工作任务。
//
// 如果管理器正在停止、epoch 表示准入关闭，或发起者 me 已取消，则拒绝登记并
// 返回 nil；成功时返回带唯一 ID 的 nativeTask，之后必须调用 finishNativeTask。
func (p *Coroutines) admitNativeTask(me Thread) *nativeTask {
	p.creationMu.RLock()
	defer p.creationMu.RUnlock()
	if p.stopping || p.abortEpoch.Load()&1 != 0 || p.isThreadCanceled(me) {
		return nil
	}
	task := &nativeTask{id: p.nextNativeID.Add(1)}
	p.threadsMu.Lock()
	p.nativeTasks[task] = struct{}{}
	p.threadsMu.Unlock()
	return task
}

// finishNativeTask 从存活原生任务表中移除 task。
// nil 表示任务从未成功准入，直接忽略即可。
func (p *Coroutines) finishNativeTask(task *nativeTask) {
	if task == nil {
		return
	}
	p.threadsMu.Lock()
	delete(p.nativeTasks, task)
	p.threadsMu.Unlock()
}

// snapshotThreads 在注册表锁内复制当前全部存活 Thread，再释放锁返回独立切片。
// 调用方可以在不阻塞并发创建/退出的情况下遍历快照，但快照会随即可能过期。
func (p *Coroutines) snapshotThreads() []Thread {
	p.threadsMu.Lock()
	threads := make([]Thread, 0, len(p.allThreads))
	for th := range p.allThreads {
		threads = append(threads, th)
	}
	p.threadsMu.Unlock()
	return threads
}

// hasThreadsOtherThan 报告是否仍存在 skip 以外的 Thread 或任意原生任务。
// skip 主要用于受管理调用者等待其他 Thread 退出时排除自身。
func (p *Coroutines) hasThreadsOtherThan(skip Thread) bool {
	p.threadsMu.Lock()
	defer p.threadsMu.Unlock()
	for th := range p.allThreads {
		if th != skip {
			return true
		}
	}
	return len(p.nativeTasks) != 0
}

// waitForThreadsToStopFromCoroutine 允许受管理的 caller 等待其他任务退出。
// 等待前必须暂时清空 current 并释放 runMu，否则其他已取消 Thread 无法获得执行权
// 进入收尾；等待结束后重新取得 runMu，并恢复 caller 的 current 身份。
func (p *Coroutines) waitForThreadsToStopFromCoroutine(timeout stime.Duration, caller Thread) bool {
	// 释放 runMu，使已取消的同伴能够继续运行并从注册表注销。
	p.setCurrent(nil)
	p.runMu.Unlock()
	completed := p.waitForThreadsToStop(timeout, caller)
	p.runMu.Lock()
	p.setCurrent(caller)
	return completed
}

// waitForThreadsToStop 轮询等待 skip 以外的全部 Thread 和原生任务退出。
// timeout 小于等于零表示无限等待；正值超时则返回 false，全部退出返回 true。
// 这里使用短间隔休眠，不参与脚本帧调度，因此调用者必须确保没有占用 runMu。
func (p *Coroutines) waitForThreadsToStop(timeout stime.Duration, skip Thread) bool {
	hasTimeout := timeout > 0
	deadline := stime.Time{}
	if hasTimeout {
		deadline = stime.Now().Add(timeout)
	}

	for {
		if !p.hasThreadsOtherThan(skip) {
			return true
		}

		sleepFor := 10 * stime.Millisecond
		if hasTimeout {
			remaining := stime.Until(deadline)
			if remaining <= 0 {
				return false
			}
			if remaining < sleepFor {
				sleepFor = remaining
			}
		}
		stime.Sleep(sleepFor)
	}
}

// runThread 是每个脚本 Thread 对应 Go goroutine 的统一入口。
//
// 它先建立 goroutine 身份映射，再取得全局 runMu 执行用户包装函数；无论函数
// 正常返回、被取消还是 panic，延迟调用的 finishThread 都会完成唯一的退出路径。
func (p *Coroutines) runThread(th Thread, fn func(me Thread) int) {
	// [消息分发 7.7] 建立“当前 Go goroutine -> Thread”映射，使 Wait、Join、
	// IsInCoroutine 等函数能准确识别调用者，而不是误用全局 Current。
	gid := gid.Get()
	p.goroutineThreads.Store(gid, th)
	// 所有脚本 goroutine 共用同一把 runMu。拿到锁之后到主动 Yield 或结束
	// 之前的代码，构成这个 Thread 的一个连续执行片段（script slice）。
	p.runMu.Lock()
	// 从这里到下一次 Yield/函数结束，这个 goroutine 是唯一允许执行脚本正文的
	// Thread；其他接收者 goroutine 即使已启动，也只能阻塞在 runMu.Lock。
	p.setCurrent(th)
	// recover 必须放在统一入口，才能同时覆盖 Before、Latch、事件适配器和用户
	// OnMsg 函数的 panic，并保证所有生命周期集合最终得到清理。
	defer func() {
		p.finishThread(th, gid, recover())
	}()

	if th.stopped.Load() {
		panic(ErrAbortThread)
	}
	// fn 通常是一个事件处理函数的包装。函数内部可以多次 Wait/Yield；这些
	// 操作会暂时释放 runMu，恢复后仍从同一个 Go 调用栈继续，不会新建 Thread。
	// [消息分发 10/11] 对消息接收者，fn 是 StartBatch 在 Create 时传入的包装：
	// 先执行 task.Before，再等待接力 Latch，最后调用 task.Run 和用户 OnMsg。
	fn(th)
}

// finishThread 完成 Thread 的永久退出和资源回收。
//
// 顺序很重要：先唤醒等待“首次让出/结束”和 Join 的 Thread，再关闭完成通道并
// 删除调度状态；随后清空 current、释放 runMu、移除 goroutine 身份，最后才处理
// panic。unregisterThread 通过 defer 放在最末，保证注册表不再包含该 Thread 时，
// 它的退出路径已经完整走完（即使 onPanic 自身再次 panic 也一样会注销）。
func (p *Coroutines) finishThread(th Thread, gid uint64, recovered any) {
	// [消息分发 15] 用户 OnMsg 返回或因取消/panic 展开调用栈后进入这里。
	// task.Run 的 defer cleanup 在此之前已经执行，messageHandlerFrames 的 defer
	// 也已删除开始帧记录；这里负责协程层面的等待者唤醒和资源回收。
	// 只有用户函数返回、被取消或发生受控 panic 时才走到这里。普通 Wait
	// 不会结束 Thread，只会让同一个 Thread 暂停并在以后继续。
	for _, waiter := range th.finishYieldWaiters() {
		p.markRunnableAndResume(waiter)
	}
	for _, waiter := range th.finishJoinWaiters() {
		p.markRunnableAndResume(waiter)
	}
	// 必须先把等待者变成 runnable，再删除目标自身的调度状态。
	th.Cancel()
	close(th.done)
	p.removeThreadState(th)
	p.setCurrent(nil)
	defer p.unregisterThread(th)
	// 到这里该 Thread 永久退出，释放 runMu 后其他已创建/已恢复的 Thread
	// 才可能取得执行权。
	p.runMu.Unlock()
	p.finalizingGoroutines.Store(gid, struct{}{})
	defer p.finalizingGoroutines.Delete(gid)
	p.goroutineThreads.Delete(gid)
	p.handleThreadPanic(th, recovered)
}

// handleThreadPanic 区分正常退出哨兵和真正的用户代码 panic。
//
// nil、ErrAbortThread 与 ErrStopThisScript 都属于预期控制流；其他值若配置了
// onPanic 就包装成 PanicReport 上报，否则在收尾 goroutine 中重新 panic。
func (p *Coroutines) handleThreadPanic(th Thread, recovered any) {
	if recovered == nil || recovered == ErrAbortThread || recovered == ErrStopThisScript {
		return
	}
	if p.onPanic != nil {
		p.onPanic(PanicReport{
			Value:         recovered,
			Name:          th.name,
			Stack:         string(sdebug.Stack()),
			CreationStack: th.stack,
		})
		return
	}
	panic(recovered)
}
