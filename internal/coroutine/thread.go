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

// ThreadObj 是 Thread 对应的业务 owner。字符串或实现 Name() 的对象也提供诊断名称。
// SPX 中常见 owner 是 *Game、*SpriteImpl 或事件派发器标记对象。
type ThreadObj any

// suspendState 描述 Thread 底层 Go goroutine 在 Yield/Resume 握手中的状态。
// 它与 scheduler.go 的 threadState 不同：后者回答 Update 是否仍需等待该 Thread。
type suspendState uint8

const (
	// suspendStateRunning 表示 goroutine 尚未在 suspendCond 上休眠。
	suspendStateRunning suspendState = iota
	// suspendStateSuspended 表示 goroutine 已经进入 Yield 等待恢复。
	suspendStateSuspended
	// suspendStateSignaled 表示 Resume 早于 Yield 到达，唤醒信号留给随后的 Yield 消费。
	suspendStateSignaled
)

// threadImpl 保存一个受管脚本 Thread 的身份、取消、挂起和等待者状态。
type threadImpl struct {
	Obj ThreadObj // 业务 owner；事件停止范围和诊断均依赖它。

	id    int64
	name  string
	stack string

	// 事件处理器重启时可继承旧执行的排序位置，但 Thread 自身 ID 不变。
	resumeOrder int64

	stopped      atomic.Bool // 已收到取消请求；恢复后以 ErrAbortThread 退出。
	suspendMu    sync.Mutex
	suspendCond  *sync.Cond
	suspendState suspendState

	ctx        context.Context // 供 WaitForChan 等外部 worker 观察取消。
	cancelFunc context.CancelFunc
	done       chan struct{} // Thread 完全退出时关闭，供外部 Join 使用。

	schedFrame     int64
	schedTimestamp stime.Time
	mainStartedAt  stime.Time

	warp            atomic.Bool  // “不刷新屏幕运行”模式。
	warpStart       atomic.Int64 // 当前 warp 时间预算窗口起点。
	stopAtNextYield atomic.Bool  // 到下一个协作让出点再取消。

	joinWaiters   waiterSet     // 等待 Thread 完全结束的受管协程。
	yieldWaiters  waiterSet     // 等待 Thread 第一次 Yield 或结束的受管协程。
	yieldedOrDone chan struct{} // 首次 Yield 或结束时关闭，供外部调用方等待。
}

// Thread 是调度器对一个脚本协程的句柄，底层实现为 *threadImpl。
type Thread = *threadImpl

// newThread 仅分配本地状态，不登记生命周期，也不启动 goroutine。
// 直接调用方：createThread；总体流程调用方：Create/StartBatch。
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

// Context 返回 Thread 的取消上下文；零值 Thread 状态回退为 Background。
func (th *threadImpl) Context() context.Context {
	if th.ctx == nil {
		return context.Background()
	}
	return th.ctx
}

// Cancel 幂等地请求停止 Thread，并唤醒可能停在 suspendCond 上的 goroutine。
// 真正退出发生在 Thread 重新取得 runMu 后检查 stopped 时。
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

// cancelContext 发布取消，解除 WaitForChan 等 worker 的外部阻塞。
func (th *threadImpl) cancelContext() {
	if th.cancelFunc != nil {
		th.cancelFunc()
	}
}

// String 返回用于日志的 Thread ID 和诊断名称。
func (th *threadImpl) String() string {
	return fmt.Sprintf("id=%d name=%s ", th.id, th.name)
}

// Name 返回由 owner 推导的诊断名称。
func (th *threadImpl) Name() string {
	return th.name
}

// ID 返回当前 Coroutines 管理器内唯一且递增的编号。
func (th *threadImpl) ID() int64 {
	return th.id
}

// Stack 返回调试模式下捕获的创建栈。
func (th *threadImpl) Stack() string {
	return th.stack
}

// Stopped 表示 Thread 是否已经收到停止请求，不代表退出收尾已经完成。
func (th *threadImpl) Stopped() bool {
	return th.stopped.Load()
}

// RunWithoutScreenRefresh 表示 Thread 是否处于 Scratch warp 模式。
func (th *threadImpl) RunWithoutScreenRefresh() bool {
	return th.warp.Load()
}

// SetRunWithoutScreenRefresh 切换 warp 模式并返回旧值；状态变化时重置预算窗口。
func (th *threadImpl) SetRunWithoutScreenRefresh(enabled bool) bool {
	previous := th.warp.Swap(enabled)
	if enabled != previous {
		th.warpStart.Store(0)
	}
	return previous
}

// ShouldWaitNextFrame 判断循环边界是否应让到下一帧。
// 普通模式始终让帧；warp 模式在时间预算耗尽前继续执行，避免每轮刷新屏幕。
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

	// 预算耗尽时强制让出一次，并为恢复后的执行建立新窗口。
	th.warpStart.Store(0)
	return true
}

// IsSchedTimeout 判断当前逻辑帧中的连续调度时间是否超过 ms；换帧时自动重置起点。
func (th Thread) IsSchedTimeout(ms float64) bool {
	frame := time.Frame()
	if th.schedFrame < frame {
		th.schedFrame = frame
		th.schedTimestamp = stime.Now()
	}
	return stime.Since(th.schedTimestamp) > stime.Duration(ms)*stime.Millisecond
}

// BeginMain 记录当前 Main 的开始时间，并返回恢复上层时间的函数，支持嵌套 Main。
func (th Thread) BeginMain(startedAt stime.Time) func() {
	previous := th.mainStartedAt
	th.mainStartedAt = startedAt
	return func() {
		th.mainStartedAt = previous
	}
}

// MainStartedAt 返回当前 Main 开始时间；零值表示已禁用 Main 超时检查。
func (th Thread) MainStartedAt() stime.Time {
	return th.mainStartedAt
}

// DisableMainTimeout 禁用当前 Main 的超时检查。
func (th Thread) DisableMainTimeout() {
	th.mainStartedAt = stime.Time{}
}

// threadNamer 允许业务 owner 提供稳定的诊断名称。
type threadNamer interface {
	Name() string
}

// resolveThreadName 按 string、Name()、具名指针类型的顺序推导诊断名称。
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
