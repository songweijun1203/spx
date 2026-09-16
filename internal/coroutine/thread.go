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
	"fmt"
	"sync"
	"sync/atomic"
	stime "time"

	"github.com/goplus/spx/v3/internal/time"
)

// ThreadObj 是与 Thread 关联的 owner。字符串值和实现 Name() 的对象还能
// 提供诊断名称；事件系统通常把 Game 或 SpriteImpl 作为 owner，用于归属、
// 命名以及按 owner 停止脚本。
type ThreadObj any

// suspendState 描述承载 Thread 的 Go goroutine 在 Yield/Resume 握手中的状态。
// 它与 scheduler.go 的 threadState 不同：前者防止条件变量丢唤醒，后者用于
// Update 判断 Thread 是否需要外部条件才能继续。
type suspendState uint8

const (
	// Running 表示 goroutine 没有停在 suspendCond 上；它仍可能正在等待
	// runMu，所以不能据此判断脚本正文是否正在执行。
	suspendStateRunning suspendState = iota
	// Suspended 表示 Yield 已经把 goroutine 挂在条件变量上，等待 Resume。
	suspendStateSuspended
	// Signaled 解决 Resume 早于 Yield 正式挂起的竞态：随后 Yield 会消费这个
	// 预存信号，而不会再次睡眠导致丢失唤醒。
	suspendStateSignaled
)

// threadImpl 是一次脚本调用实例的完整运行时状态。
// 同一个 OnStart/OnCloned handler 每触发一次都会创建新的 threadImpl。
type threadImpl struct {
	// Obj 是脚本 owner，通常为 Game、SpriteImpl 或诊断用字符串。
	Obj ThreadObj

	// id 在 newThread 时单调递增，表示 Thread（一次脚本调用实例）的创建/
	// 注册先后。它不同于 WaitJob.Id，也不同于事件 sink 的登记编号。
	id int64
	// name 是从 Obj 解析并缓存的诊断名称。
	name string
	// stack 是 debug 模式下创建 Thread 时捕获的调用栈。
	stack string

	// stopped 表示已经请求永久终止；一旦为 true 不会恢复为 false。
	stopped atomic.Bool
	// suspended 是供诊断和测试快速读取的“当前停在条件变量上”标志。
	suspended atomic.Bool
	// suspendMu 保护 suspendState，并作为 suspendCond 的关联锁。
	suspendMu sync.Mutex
	// suspendCond 负责 Yield 与 Resume 之间的实际睡眠/唤醒。
	suspendCond *sync.Cond
	// suspendState 保存 running/suspended/signaled 精确握手状态。
	suspendState suspendState

	// ctx 在 Thread 被停止时取消，供异步等待和子任务观察退出。
	ctx context.Context
	// cancelFunc 关闭 ctx；由 Cancel/finishThread 调用。
	cancelFunc context.CancelFunc
	// done 只在线程永久结束时关闭；普通 Wait/Yield 不会关闭它。
	done chan struct{}

	// schedFrame 是 IsSchedTimeout 上次初始化计时窗口时的引擎帧号。
	schedFrame int64
	// schedTimestamp 是当前帧调度超时窗口的起始墙钟时间。
	schedTimestamp stime.Time

	// runWithoutScreenRefresh 表示 Scratch warp/不刷新屏幕模式是否开启。
	runWithoutScreenRefresh atomic.Bool
	// runWithoutScreenRefreshStart 保存当前 warp 连续执行窗口的起点纳秒值；
	// 超过预算后必须强制让出一次，避免长期占用引擎。
	runWithoutScreenRefreshStart atomic.Int64
	// stopAtNextYield 表示允许当前片段跑到下一个协作式让出点，再执行取消。
	stopAtNextYield atomic.Bool

	// waitersMu 保护下面两类等待者集合以及 yieldedOrDone 的一次性发布。
	waitersMu sync.Mutex
	// joinDone 表示该 Thread 已永久完成，后续 Join 无需登记等待者。
	joinDone bool
	// joinWaiters 保存等待本 Thread 永久完成的其他受管 Thread。
	joinWaiters map[Thread]struct{}

	// yieldedOrDone 在第一次 Yield 或永久结束时关闭，用于 CreateAndStart 和
	// BatchWaitFirstSlice 一类“只等第一次执行片段”的同步，而不是等待整个
	// forever 脚本结束。
	yieldedOrDoneOnce sync.Once
	// yieldedOrDone 在第一次 Yield 或永久完成时关闭，供非协程调用者等待。
	yieldedOrDone chan struct{}
	// yieldWaiters 保存等待本 Thread 第一次 Yield/完成的其他受管 Thread。
	yieldWaiters map[Thread]struct{}
}

// Thread 是 threadImpl 指针的公开别名，表示一个可暂停、恢复和取消的脚本协程。
type Thread = *threadImpl

// Context 返回 Thread 的取消上下文。零值/测试构造 Thread 没有 ctx 时返回
// Background，避免调用者判空。
func (th *threadImpl) Context() context.Context {
	if th.ctx == nil {
		return context.Background()
	}
	return th.ctx
}

// Cancel 取消 Thread 上下文。它只广播取消信号；真正停止脚本还依赖 stopped、
// 条件变量唤醒以及脚本到达可检查取消的位置。
func (th *threadImpl) Cancel() {
	if th.cancelFunc != nil {
		th.cancelFunc()
	}
}

// String 返回包含 Thread ID 和诊断名称的文本，主要用于日志和测试失败信息。
func (th *threadImpl) String() string {
	return fmt.Sprintf("id=%d name=%s ", th.id, th.name)
}

// Name 返回创建 Thread 时从 owner 解析出的诊断名称。
func (th *threadImpl) Name() string {
	return th.name
}

// ID 返回该协程管理器内单调递增的 Thread ID。它表示 Thread 创建顺序，
// 不表示 WaitJob 入队顺序或 goroutine 实际获得 runMu 的顺序。
func (th *threadImpl) ID() int64 {
	return th.id
}

// Stack 返回 debug 模式下捕获的创建栈；未启用时为空字符串。
func (th *threadImpl) Stack() string {
	return th.stack
}

// Stopped 报告该 Thread 是否已经收到永久停止请求。
func (th *threadImpl) Stopped() bool {
	return th.stopped.Load()
}

// RunWithoutScreenRefresh 报告 Thread 是否处于 Scratch warp/不刷新屏幕模式。
func (th *threadImpl) RunWithoutScreenRefresh() bool {
	return th.runWithoutScreenRefresh.Load()
}

// SetRunWithoutScreenRefresh 修改 warp 模式并返回旧值。模式发生变化时重置
// 连续运行计时窗口，避免把上一次 warp 的耗时带入新的窗口。
func (th *threadImpl) SetRunWithoutScreenRefresh(enabled bool) bool {
	previous := th.runWithoutScreenRefresh.Swap(enabled)
	if enabled != previous {
		th.runWithoutScreenRefreshStart.Store(0)
	}
	return previous
}

// ShouldWaitNextFrame 判断 Thread 在当前循环边界是否应强制等到下一帧。
// 普通模式始终返回 true；warp 模式在预算内返回 false，超过预算后返回 true
// 并重置计时窗口，使长时间 warp 计算仍会周期性归还引擎执行权。
func (th *threadImpl) ShouldWaitNextFrame(runWithoutScreenRefreshBudget stime.Duration) bool {
	if !th.RunWithoutScreenRefresh() {
		return true
	}

	now := stime.Now().UnixNano()
	startedAt := th.runWithoutScreenRefreshStart.Load()
	if startedAt == 0 {
		th.runWithoutScreenRefreshStart.Store(now)
		return false
	}
	if stime.Duration(now-startedAt) <= runWithoutScreenRefreshBudget {
		return false
	}

	// warp 预算耗尽后强制让出一次，并从下次调用开始建立新窗口。
	th.runWithoutScreenRefreshStart.Store(0)
	return true
}

// IsSchedTimeout 判断当前引擎帧内，该 Thread 的调度执行是否已经超过 ms。
// 帧号变化时重新记录起点；它用于检测单帧长时间执行，而不是 wait 的计时。
func (th Thread) IsSchedTimeout(ms float64) bool {
	frame := time.Frame()
	if th.schedFrame < frame {
		th.schedFrame = frame
		th.schedTimestamp = stime.Now()
	}
	return stime.Since(th.schedTimestamp) > stime.Duration(ms)*stime.Millisecond
}
