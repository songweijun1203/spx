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
	"errors"
	sdebug "runtime/debug"
	"sync"
	"sync/atomic"
	stime "time"
)

var (
	// ErrCannotYieldANonrunningThread 表示调用 Yield 的协程并不持有当前脚本执行权。
	ErrCannotYieldANonrunningThread = errors.New("can not yield a non-running thread")

	// ErrAbortThread 是彻底终止当前 Thread 时使用的内部 panic 哨兵。
	ErrAbortThread = errors.New("abort thread")

	// ErrStopThisScript 是 Scratch “停止这个脚本”向过程/事件边界传播的 panic 哨兵。
	ErrStopThisScript = errors.New("stop this script")

	// ErrReentrantWait 表示回调尝试同步等待自身释放的资源，继续等待将形成死锁。
	ErrReentrantWait = errors.New("coroutine: callback cannot synchronously wait for itself")
)

// PanicReport 保存未处理的脚本 panic 及诊断上下文，由 internal/engine.OnPanic 消费。
type PanicReport struct {
	Value         any    // recover 得到的 panic 值。
	Name          string // Thread owner 推导出的脚本名称。
	Stack         string // recover 时捕获的 panic 现场栈。
	CreationStack string // 调试模式下创建 Thread 时捕获的可选栈。
}

// Coroutines 统一管理 Thread 生命周期、协作式执行权、等待队列及停止屏障。
//
// 直接创建方：根包 initGameRuntime 中的 coroutine.New；顶层驱动方：
// internal/engine.onUpdate 每帧调用 Update。锁顺序必须保持
// shutdownMu -> runMu -> admissionMu，其他细粒度锁不得反向取得这三个锁。
type Coroutines struct {
	onPanic func(PanicReport) // 非控制流 panic 的最终上报入口。
	debug   bool              // 是否记录 Thread 创建栈。

	// runMu 串行化所有脚本执行片段；current 指向当前持锁者。
	runMu   sync.Mutex
	current atomic.Pointer[threadImpl] //当某个协程开始执行时候，会set为当前协程，协程结束后会清空

	// shutdownMu 串行化外部排空操作；admissionMu 保护新任务准入及 stopping。
	shutdownMu  sync.Mutex
	admissionMu sync.RWMutex
	stopping    bool // 在 admissionMu 保护下为 true 时，准入一定已经关闭。

	// threadsMu 保护 Thread/worker 生命周期登记和排空变化通知。
	threadsMu        sync.Mutex
	allThreads       map[Thread]struct{}
	workerCount      int           // WaitToDo 等外部 worker 的数量。
	lifecycleChanged chan struct{} // 排空时按需创建；有任务退出时关闭并置空。

	// schedulerMu 保证“修改 runnable 状态 + 发布 WaitJob”对 Update 原子可见。
	schedulerMu     sync.Mutex
	schedulerCond   *sync.Cond
	runnableThreads map[Thread]struct{} // Update 仍需等待其让出/结束的 Thread 集合。
	currentJobs     *Queue[*WaitJob]    // 本次 Update 可以检查和执行的任务。
	deferredJobs    *Queue[*WaitJob]    // 尚未到期，留给后续引擎帧的任务。
	roundJobs       *Queue[*WaitJob]    // 可能在当前帧额外脚本轮次恢复的任务。
	redrawFrame     atomic.Int64        // 本帧请求重绘后，不再开启额外脚本轮次。
	scriptRound     atomic.Uint64       // 不推进引擎帧的额外脚本轮次计数。

	nextThreadID atomic.Int64 // 当前管理器内单调递增的 Thread ID。

	// admissionEpoch 偶数表示开放、奇数表示关闭。StopAll 后仍未返回的 Setup 会延迟重开。
	admissionEpoch atomic.Uint64
	pendingSetups  atomic.Int64 // 已准入但 Setup 尚未返回的任务数。

	perfDebug         atomic.Bool           // 是否在 Update 前后读取 GC 统计。
	readGCStats       func(*sdebug.GCStats) // 可替换以便测试。
	updateWatchdogNow func() stime.Time     // Update 一秒保护计时源。
	statsMu           sync.RWMutex
	lastUpdateStats   UpdateJobsStats

	// 调用方身份独立于 current；前者回答“当前 goroutine 属于谁”，后者回答“runMu 属于谁”。
	goroutineThreads sync.Map // map[uint64]Thread

	// callbacks 记录当前 goroutine 所处的运行时回调范围，用于禁止自等待和重入排空。
	callbacks sync.Map // map[uint64]callbackScope
}

// New 创建一套独立调度器。Thread 遇到非控制流 panic 时调用 onPanic。
//
// 直接调用方：根包 initGameRuntime 和测试；总体流程调用方：一局游戏运行时初始化。
// 三个 WaitJob 队列共享节点池，因为任务会在 current/deferred/round 之间频繁移动。
func New(onPanic func(PanicReport)) *Coroutines {
	// WaitJob 节点会在三个队列间移动，因此共享同一个回收池。
	jobs := NewQueue[*WaitJob]()
	p := &Coroutines{
		onPanic:           onPanic,
		allThreads:        make(map[Thread]struct{}),
		runnableThreads:   make(map[Thread]struct{}),
		currentJobs:       jobs,
		deferredJobs:      &Queue[*WaitJob]{pool: jobs.pool},
		roundJobs:         &Queue[*WaitJob]{pool: jobs.pool},
		readGCStats:       sdebug.ReadGCStats,
		updateWatchdogNow: stime.Now,
	}
	p.schedulerCond = sync.NewCond(&p.schedulerMu)
	p.redrawFrame.Store(-1)
	return p
}

// ScriptRound 返回跨引擎帧累计的额外脚本轮次编号。
// 应与逻辑 frame 一起使用，才能唯一标识一次调度轮次。
func (p *Coroutines) ScriptRound() uint64 {
	return p.scriptRound.Load()
}

// SetPerfDebug 控制 Update 是否额外采集 GC 次数和暂停时间。
func (p *Coroutines) SetPerfDebug(enabled bool) {
	p.perfDebug.Store(enabled)
}

// IsAbortThreadError 判断值是否为“终止整个 Thread”的内部控制流哨兵。
func IsAbortThreadError(err any) bool {
	return err == ErrAbortThread
}

// IsStopThisScriptError 判断值是否为“停止当前脚本”的内部控制流哨兵。
func IsStopThisScriptError(err any) bool {
	return err == ErrStopThisScript
}
