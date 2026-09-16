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
	// ErrCannotYieldANonrunningThread 表示调用者不是当前运行中的受管 Thread，
	// 却尝试执行 Yield。只有持有 runMu 且 Current() 指向自己的 Thread 能让出。
	ErrCannotYieldANonrunningThread = errors.New("can not yield a non-running thread")

	// ErrAbortThread 是终止整个 Thread 的内部哨兵 panic。生命周期边界会吞掉
	// 该值，不把它当作用户脚本异常上报。
	ErrAbortThread = errors.New("abort thread")

	// ErrStopThisScript 是 Scratch“停止当前脚本”的内部哨兵 panic。它可以在
	// 最近的过程/事件边界被消费，而不一定像 ErrAbortThread 一样取消所有上下文。
	ErrStopThisScript = errors.New("stop this script")
)

// PanicReport 保存未被协程边界处理的 panic 及其诊断信息。
type PanicReport struct {
	// Value 是 recover 得到的原始 panic 值。
	Value any
	// Name 是 Thread owner 解析出的诊断名称，例如精灵名或显式字符串。
	Name string
	// Stack 是处理 panic 时捕获的当前调用栈，尽量接近故障位置。
	Stack string
	// CreationStack 是 debug 模式下创建 Thread 时保存的可选调用栈，用于追踪
	// “这个异步脚本最初由谁创建”。
	CreationStack string
}

// Coroutines 统一管理 Thread 生命周期、协作式让出、跨帧等待和恢复顺序。
//
// 可以把它看成 SPX 脚本虚拟机的调度核心。这里需要区分三个概念：
//
//  1. 事件处理函数：OnStart/OnCloned 等注册到事件表中的普通 Go 函数；
//     注册处理函数时还没有创建 Thread，也没有开始执行脚本。
//  2. Thread：某次事件真正触发时，为一个匹配的处理函数创建的一次执行实例；
//     同一个事件处理函数被触发两次，会得到两个不同的 Thread。
//  3. Go goroutine：每个 Thread 底层由一个 goroutine 承载，但 runMu 保证
//     同一时刻只有一个 Thread 能执行用户脚本，因此脚本正文不是并行运行的。
//
// 一个 Thread 通常按如下状态循环：
//
//	创建并登记为 runnable
//	       -> 获得 runMu，执行一段脚本
//	       -> Wait/循环边界把自己标记为 blocked，并提交 WaitJob
//	       -> Yield 释放 runMu
//	       -> WaitJob 满足条件后被 Resume，再次等待 runMu
//	       -> 继续执行，最终返回并注销 Thread
//
// “一段脚本”指从获得 runMu 开始，到下一次 Yield 或函数结束为止。后文所说
// 的 first slice（第一次执行片段）也是这个含义。
type Coroutines struct {
	// onPanic 接收未处理的脚本 panic；为 nil 时会在清理后重新 panic。
	onPanic func(PanicReport)
	// hasInited 表示引擎初始化是否完成。初始化完成前 Update 在空队列时采用
	// 特殊等待逻辑，避免启动阶段忙等。
	hasInited atomic.Bool
	// debug 控制是否在创建 Thread 时记录 CreationStack。
	debug bool

	// runMu 是所有脚本执行片段共享的互斥锁，保证脚本正文串行运行。
	// 每个 Thread 虽然各有一个 Go goroutine，但进入用户脚本前都要先获得
	// runMu；Wait/Yield 时释放它，恢复后还要重新竞争它。
	runMu sync.Mutex
	// current 只表示当前真正持有 runMu 的 Thread，不表示所有已经收到
	// Resume 信号、正等待 runMu 的 Thread。
	current atomic.Pointer[threadImpl]

	// shutdownMu 串行化完整的停止/清理流程，防止两个 AbortAllAndWait 同时
	// 驱动生命周期收尾。
	shutdownMu sync.Mutex
	// creationMu 保护“是否允许创建”和“登记 Thread”成为一个原子过程。
	// 多锁同时使用时顺序固定为 shutdownMu -> runMu -> creationMu。
	creationMu sync.RWMutex
	// stopping 表示管理器正处于长期停止屏障中，新 Thread 和 nativeTask 会被拒绝。
	stopping bool

	// threadsMu 保护 allThreads 和 nativeTasks 两个生命周期集合。
	threadsMu sync.Mutex
	// allThreads 保存所有已登记且尚未完成注销的受管 Thread。
	allThreads map[Thread]struct{}
	// nativeTasks 保存 WaitToDo 启动、但尚未结束的非调度器工作单元。
	nativeTasks map[*nativeTask]struct{}

	// schedulerMu 保护 threadStates 和 schedulerCond 的条件谓词，并让
	// “修改 Thread 状态 + 发布对应 WaitJob”成为原子操作。
	schedulerMu sync.Mutex
	// schedulerCond 在队列、Thread 状态或生命周期变化时唤醒 Update。
	schedulerCond *sync.Cond
	// threadStates 保存每个已登记 Thread 的 runnable/blocked 调度状态。
	// threadStates 只记录调度角度的 runnable/blocked。runnable 的含义是
	// “允许继续运行”，并不等于“此刻已经获得 runMu 正在执行”。
	threadStates map[Thread]threadState
	// [WaitJob 流程 1] currentJobs 是本次 Update 正在从队头检查的 WaitJob 队列。
	// Wait/WaitNextFrame/WaitYield/循环边界创建 Job 后，通常先把它追加到这里；
	// 上一帧未满足条件的 deferredJobs 和 loopJobs 也会在帧末搬回这里，等待
	// 下一次 Update 重新检查。
	currentJobs *Queue[*WaitJob]
	// [WaitJob 流程 4B] deferredJobs 保存本次 Update 检查后仍未满足帧号或时间
	// 条件的 WaitJob。runUpdateLoop 不会在同一次 Update 再读取这个队列；只有
	// promoteDeferredJobs 在收尾时把它搬回 currentJobs，所以下次检查至少发生
	// 在下一次 gco.Update。
	deferredJobs *Queue[*WaitJob]
	// [WaitJob 流程 4C] loopJobs 保存 forever/repeat 在“当前物理帧”到达循环
	// 边界后提交的续跑任务。currentJobs 和 runnable Thread 清空后，调度器才
	// 判断是否开启下一脚本轮次：若本帧没有请求重绘且预算充足，它们转回
	// currentJobs，在同一物理帧再执行一轮；否则帧末并入 deferredJobs。
	loopJobs *Queue[*WaitJob]
	// redrawFrame 记录最近一次 RequestRedraw 对应的引擎帧号。它用于阻止
	// 普通循环在已经产生视觉变化的同一帧继续开启额外轮次。
	redrawFrame atomic.Int64

	// nextJobID 为 WaitJob 分配管理器内单调递增的创建序号。
	nextJobID atomic.Int64
	// nextThreadID 为 Thread 分配管理器内单调递增的注册序号。它与 Job ID
	// 不是同一种顺序：后来创建的克隆 Thread 可能更早提交 WaitJob。
	nextThreadID atomic.Int64
	// nextNativeID 为 WaitToDo 的 nativeTask 分配诊断和登记序号。
	nextNativeID atomic.Uint64
	// abortEpoch 是创建准入屏障的版本号：偶数表示开放，奇数表示关闭。
	// Create 在登记前后比较版本，保证与 AbortAll 重叠的创建不会逃过取消快照。
	abortEpoch atomic.Uint64

	// perfDebug 控制是否在每次 Update 前后读取 GC 统计。
	perfDebug atomic.Bool
	// readGCStats 默认指向 runtime/debug.ReadGCStats，抽成字段便于测试替换。
	readGCStats func(*sdebug.GCStats)
	// updateWatchdogNow 默认取 time.Now，抽成字段便于测试控制看门狗时间。
	updateWatchdogNow func() stime.Time
	// statsMu 保护 lastUpdateStats 的发布和读取。
	statsMu sync.RWMutex
	// lastUpdateStats 是最近一次完整 Update 的性能统计快照。
	lastUpdateStats UpdateJobsStats

	// goroutineThreads 把 Go goroutine ID 映射到它承载的精确 Thread。
	// 不能只用全局 Current 判断调用者，因为外部 goroutine 也可能并发读取它，
	// 而等待恢复的受管 goroutine 需要识别自己的 Thread 身份。
	goroutineThreads sync.Map // map[uint64]Thread
	// finalizingGoroutines 记录正在执行 panic 收尾回调的 goroutine，防止这些
	// 回调重入 RunAfterAbortAll 等不允许从协程收尾上下文调用的流程。
	finalizingGoroutines sync.Map // map[uint64]struct{}
}

// New 创建一个协程管理器。某个 Thread 因未处理 panic 退出时会调用 onPanic；
// ErrAbortThread 和 ErrStopThisScript 属于正常控制流，不会上报。
func New(onPanic func(PanicReport)) *Coroutines {
	p := &Coroutines{
		onPanic:           onPanic,
		allThreads:        make(map[Thread]struct{}),
		nativeTasks:       make(map[*nativeTask]struct{}),
		threadStates:      make(map[Thread]threadState),
		currentJobs:       NewQueue[*WaitJob](),
		deferredJobs:      NewQueue[*WaitJob](),
		loopJobs:          NewQueue[*WaitJob](),
		readGCStats:       sdebug.ReadGCStats,
		updateWatchdogNow: stime.Now,
	}
	p.schedulerCond = sync.NewCond(&p.schedulerMu)
	p.redrawFrame.Store(-1)
	return p
}

// OnRestart 把调度器标记为尚未完成初始化，供游戏重启流程使用。
func (p *Coroutines) OnRestart() {
	p.hasInited.Store(false)
}

// OnInited 把调度器标记为初始化完成，之后 Update 使用正常的条件变量等待逻辑。
func (p *Coroutines) OnInited() {
	p.hasInited.Store(true)
}

// SetPerfDebug 开关 Update 期间的 GC 统计采集。
func (p *Coroutines) SetPerfDebug(enabled bool) {
	p.perfDebug.Store(enabled)
}

// IsAbortThreadError 判断 err 是否为“终止整个 Thread”的内部哨兵值。
func IsAbortThreadError(err any) bool {
	return err == ErrAbortThread
}

// IsStopThisScriptError 判断 err 是否为 Scratch“停止当前脚本”的内部哨兵值。
func IsStopThisScriptError(err any) bool {
	return err == ErrStopThisScript
}
