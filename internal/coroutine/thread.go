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
	"reflect"
	"sync"
	"sync/atomic"
	stime "time"

	"github.com/goplus/spx/v3/internal/debug"
	"github.com/goplus/spx/v3/internal/time"
)

// ThreadObj 是与 Thread 关联的所有者。字符串值以及实现 Name() 的所有者还会
// 用来生成 Thread 的诊断名称。
type ThreadObj any

type suspendState uint8

const (
	suspendStateRunning   suspendState = iota //表示当前没有正式挂起，也没有待消费的唤醒
	suspendStateSuspended                     //表示已经登记为挂起，可能正在 Cond.Wait
	suspendStateSignaled                      //表示 Resume 已经到达，但 Thread 还没正式进入 Cond.Wait 这是一个“记住唤醒”的状态
)

type threadImpl struct {
	Obj ThreadObj

	id    int64
	name  string
	stack string

	// 正在运行的事件处理函数重启时继承这个恢复顺序，但不改变 Thread 身份。
	resumeOrder int64

	// stopped 是 Thread 已被正式 Cancel 的状态。它与 stopAtNextYield 不同：前者
	// 表示必须退出，后者只是尚未执行的“在下一次 Yield 终止”预约。
	stopped atomic.Bool
	// suspendMu、suspendCond 和 suspendState 共同管理这个 Thread 自己的睡眠、
	// 提前唤醒和正常恢复状态；它们不负责授予全局脚本执行权。
	suspendMu    sync.Mutex
	suspendCond  *sync.Cond
	suspendState suspendState //协程的当前状态

	ctx        context.Context
	cancelFunc context.CancelFunc
	done       chan struct{}

	// schedFrame 和 schedTimestamp 用于 IsSchedTimeout：前者记录开始计时的物理
	// 帧，后者记录该帧第一次检查时的墙钟时间。
	schedFrame     int64
	schedTimestamp stime.Time

	// warp 表示“不刷新屏幕”模式。该模式不会在每次循环边界都让出。
	warp atomic.Bool
	// warpStart 是当前 Warp 连续执行窗口的墙钟起点（Unix 纳秒）；0 表示尚未
	// 开始或上一窗口已经耗尽。
	warpStart atomic.Int64
	// stopAtNextYield 是一次性延迟终止预约：设为 true 不代表 Thread 已停止；它会
	// 继续执行到下一次 Yield，由 Yield 用 Swap(false) 消费预约、调用 Cancel，并
	// 在恢复全局串行锁后通过 ErrAbortThread 彻底退出。若此前自然 return 则不触发。
	stopAtNextYield atomic.Bool

	joinWaiters   waiterSet
	yieldWaiters  waiterSet
	yieldedOrDone chan struct{}
}

// Thread 表示一个脚本协程。
type Thread = *threadImpl

func (p *Coroutines) newThread(obj ThreadObj) Thread {
	th := &threadImpl{
		Obj:           obj,
		id:            p.nextThreadID.Add(1),
		schedFrame:    -1,
		name:          resolveThreadName(obj),
		done:          make(chan struct{}),
		yieldedOrDone: make(chan struct{}),
	}
	th.resumeOrder = th.id
	th.ctx, th.cancelFunc = context.WithCancel(context.Background())
	if p.debug {
		th.stack = debug.GetStackTrace()
	}
	th.suspendCond = sync.NewCond(&th.suspendMu)
	return th
}

// Context 返回 Thread 的取消上下文。
func (th *threadImpl) Context() context.Context {
	if th.ctx == nil {
		return context.Background()
	}
	return th.ctx
}

// Cancel 把 Thread 置为正式取消状态，同时取消 Context；如果它正在条件变量上
// 等待，则将其唤醒以进入退出流程。Cancel 本身不会粗暴终止 Go goroutine：运行中
// 的 Thread 会在协作式 Yield 边界观察 stopped，挂起中的 Thread 被唤醒后观察它，
// 最终都通过 ErrAbortThread/finishThread 完成统一清理。
func (th *threadImpl) Cancel() {
	th.suspendMu.Lock()
	defer th.suspendMu.Unlock()
	if th.stopped.Load() {
		return
	}
	th.stopped.Store(true)
	th.cancelContext()
	th.suspendCond.Signal()
}

func (th *threadImpl) cancelContext() {
	if th.cancelFunc != nil {
		th.cancelFunc()
	}
}

// String 返回 Thread 的 ID 和名称。
func (th *threadImpl) String() string {
	return fmt.Sprintf("id=%d name=%s ", th.id, th.name)
}

// Name 返回解析后的 Thread 名称。
func (th *threadImpl) Name() string {
	return th.name
}

// ID 返回当前 Coroutines 管理器内唯一的 Thread ID。
func (th *threadImpl) ID() int64 {
	return th.id
}

// Stack 返回创建 Thread 时捕获的调用栈；未启用调试时为空。
func (th *threadImpl) Stack() string {
	return th.stack
}

// Stopped 报告 Thread 是否已经收到停止请求。
func (th *threadImpl) Stopped() bool {
	return th.stopped.Load()
}

// RunWithoutScreenRefresh 报告 Thread 是否处于 Warp（不刷新屏幕）模式。
func (th *threadImpl) RunWithoutScreenRefresh() bool {
	return th.warp.Load()
}

// SetRunWithoutScreenRefresh 修改 Warp 模式并返回旧值。每次模式发生切换时清零
// warpStart，使下一个循环边界重新建立 500ms 连续执行窗口。
func (th *threadImpl) SetRunWithoutScreenRefresh(enabled bool) bool {
	previous := th.warp.Swap(enabled)
	if enabled != previous {
		th.warpStart.Store(0)
	}
	return previous
}

// ShouldWaitNextFrame 判断 Thread 到达循环边界时是否必须让出到下一物理帧。
//
// 普通模式始终返回 true，但 Forever/Repeat 的普通路径不会调用本函数，而是使用
// YieldLoopFor 进入允许同帧轮次的 waitTypeLoop。Warp 模式才使用这里的独立预算：
//
//  1. 第一次检查用 time.Now().UnixNano() 记录当前墙钟起点，返回 false；
//  2. 之后用 now-startedAt 计算本 Thread 已连续执行多久；
//  3. 只要耗时小于等于传入的 budget（引擎目前传入 500ms），就继续执行；
//  4. 超过 budget 后清零起点并返回 true，调用者据此 WaitNextFrameFor；
//  5. 下一物理帧恢复后，再从新的墙钟窗口开始计算。
//
// 这份 500ms 预算属于单个 Warp Thread，不是普通模式由 beginUpdate 建立、所有
// loop 共享的 25ms workDeadline。
func (th *threadImpl) ShouldWaitNextFrame(budget stime.Duration) bool {
	if !th.RunWithoutScreenRefresh() {
		return true
	}

	now := stime.Now().UnixNano()
	startedAt := th.warpStart.Load()
	if startedAt == 0 {
		th.warpStart.Store(now)
		return false
	}
	if stime.Duration(now-startedAt) <= budget {
		return false
	}

	// Warp 预算耗尽后强制跨帧让出一次，并让下一次检查建立新的计时窗口。
	th.warpStart.Store(0)
	return true
}

// IsSchedTimeout 报告当前调度物理帧内是否已经经过 ms 毫秒。进入新物理帧时
// 会重置墙钟计时起点；这套超时统计也不参与普通 forever 的 25ms 轮次判断。
func (th Thread) IsSchedTimeout(ms float64) bool {
	frame := time.Frame()
	if th.schedFrame < frame {
		th.schedFrame = frame
		th.schedTimestamp = stime.Now()
	}
	return stime.Since(th.schedTimestamp) > stime.Duration(ms)*stime.Millisecond
}

type threadNamer interface {
	Name() string
}

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
