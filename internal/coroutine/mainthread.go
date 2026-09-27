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
	"runtime"
	"sync/atomic"
	"time"

	"github.com/goplus/spx/v3/internal/engine/platform"
)

// WaitMainThread 在 Godot/引擎主线程同步执行 call；平台允许时直接调用。
//
// 直接调用方：internal/engine.WaitMainThread；总体流程调用方：所有 Godot 节点、资源和
// Manager 操作。受管脚本等待期间继续持有 runMu，不像 Wait/WaitForChan 那样 Yield；
// 这样可保证一次业务操作中的“提交主线程任务 -> 取得结果”不会被其他脚本插入。
// native 平台由 Update 或 pumpMainThread 消费队首任务；Web/pure 通常直接进入引擎调用。
func (p *Coroutines) WaitMainThread(call func()) {
	if platform.TryCallEngineDirectly(call) {
		return
	}

	pending := &mainThreadCall{
		caller: p.callerThread(),
		done:   make(chan taskResult, 1),
	}
	p.enqueuePriorityJob(&WaitJob{
		Th:   pending.caller,
		Type: waitTypeMainThread,
		Call: func() { pending.run(p, call) },
	})
	pending.wait(p)
}

// mainThreadCall 保存一次主线程调用的原始脚本调用方、结果通道和抢占状态。
// claimed 保证“主线程开始执行”和“调用方取消跳过任务”只有一方取得执行权。
type mainThreadCall struct {
	caller  Thread
	done    chan taskResult
	claimed atomic.Bool
}

// run 在引擎线程执行 call，并把正常返回、panic 或 Goexit 结果通知等待方。
func (c *mainThreadCall) run(p *Coroutines, call func()) {
	if !c.claimed.CompareAndSwap(false, true) {
		return
	}
	result := taskResult{}
	// 即使 call 通过 runtime.Goexit 结束引擎 goroutine，也必须通知等待者。
	defer func() { c.done <- result }()
	if p.isThreadCanceled(c.caller) {
		return
	}
	if c.caller != nil {
		id, previous := p.enterCallback(callbackExclusive)
		defer p.leaveCallback(id, previous)
	}
	result = p.runExternalTask(call)
}

// wait 同步等待主线程结果；取消若与已经开始的 call 竞争失败，仍要等 call 返回，
// 以保证脚本释放 runMu 前不留下仍在操作共享引擎状态的回调。
func (c *mainThreadCall) wait(p *Coroutines) {
	var canceled <-chan struct{}
	if c.caller != nil {
		canceled = c.caller.Context().Done()
	}
	select {
	case result := <-c.done:
		if p.isThreadCanceled(c.caller) {
			panic(ErrAbortThread)
		}
		if result.panicked {
			panic(result.panicValue)
		}
	case <-canceled:
		if !c.claimed.CompareAndSwap(false, true) {
			// call 已开始时继续保留脚本执行权，直到主线程操作真正返回。
			<-c.done
		}
		panic(ErrAbortThread)
	}
}

// RunBetweenScripts 在两个脚本执行片段之间独占 runMu 执行 call。
//
// 直接调用方：条件事件采样等需要读取稳定业务状态的引擎流程；总体流程调用方：
// Godot 每帧回调。native 调用方必须位于引擎主线程；等待 runMu 时会持续泵送队首
// 主线程任务，使正等待引擎结果的脚本有机会结束并释放 runMu。已经处于脚本或独占
// 回调中再次调用会形成自等待，因此以 ErrReentrantWait 拒绝。
func (p *Coroutines) RunBetweenScripts(call func()) {
	if p.callerThread() != nil || p.currentCallback()&callbackExclusive != 0 {
		panic(ErrReentrantWait)
	}
	if hasMainThreadQueue {
		for !p.runMu.TryLock() {
			// 服务主线程调用，使等待引擎结果的脚本能够完成操作并释放 runMu。
			p.pumpMainThread()
		}
	} else {
		p.runMu.Lock()
	}
	defer p.runMu.Unlock()
	id, previous := p.enterCallback(callbackExclusive)
	defer p.leaveCallback(id, previous)
	call()
}

// TryRunFromEngine 尝试从引擎线程创建受管 Thread 执行 call，并同步等到它结束。
//
// 直接调用方：engine.execute 和事件注册屏障；总体流程调用方：Godot 回调需要进入 Go
// 脚本串行区时。等待期间只服务主线程任务，不推进 frame 或脚本轮次。返回 true 表示
// 当前平台已处理这次请求；停止期间即使跳过 call，也视为 handled。
func (p *Coroutines) TryRunFromEngine(owner ThreadObj, call func()) bool {
	if p.admissionClosed() {
		return true
	}
	if p.callerThread() != nil || p.currentCallback()&callbackExclusive != 0 {
		panic(ErrReentrantWait)
	}
	return platform.TryCallEngineDirectly(func() {
		// 初次检查之后停止流程仍可能关闭准入，因此进入引擎线程后再次确认。
		if p.admissionClosed() {
			return
		}
		dispatcher := p.Create(owner, func(Thread) {
			call()
		})
		p.joinOnEngine(dispatcher)
	})
}

// joinOnEngine 在不推进帧的前提下等待 Thread 完成，并持续解除其主线程调用依赖。
func (p *Coroutines) joinOnEngine(thread Thread) {
	if !hasMainThreadQueue {
		<-thread.done
		return
	}
	for {
		select {
		case <-thread.done:
			return
		default:
		}
		p.pumpMainThread()
	}
}

// lockShutdown 在超时预算内取得全局停止屏障；引擎线程等待时继续泵送主线程任务。
// 返回的 remaining 是取得锁后留给实际排空的剩余时间。
func (p *Coroutines) lockShutdown(timeout time.Duration) (remaining time.Duration, locked bool) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	tryLock := func(wait func()) bool {
		for !p.shutdownMu.TryLock() {
			if !deadline.IsZero() && time.Now().After(deadline) {
				return false
			}
			wait()
		}
		return true
	}
	var acquired bool
	if hasMainThreadQueue && platform.TryCallEngineDirectly(func() {
		acquired = tryLock(p.pumpMainThread)
	}) {
		locked = acquired
	} else if deadline.IsZero() {
		p.shutdownMu.Lock()
		locked = true
	} else {
		locked = tryLock(func() {
			time.Sleep(min(time.Millisecond, time.Until(deadline)))
		})
	}
	if !locked || deadline.IsZero() {
		return 0, locked
	}
	remaining = time.Until(deadline)
	if remaining > 0 {
		return remaining, true
	}
	p.shutdownMu.Unlock()
	return 0, false
}

// waitForDrainChange 等待 Thread/worker 数量变化；引擎线程等待时继续服务主线程任务。
func (p *Coroutines) waitForDrainChange(changed <-chan struct{}, timedOut <-chan time.Time) bool {
	wasChanged := false
	if hasMainThreadQueue && platform.TryCallEngineDirectly(func() {
		for {
			select {
			case <-changed:
				wasChanged = true
				return
			case <-timedOut:
				return
			default:
				p.pumpMainThread()
			}
		}
	}) {
		return wasChanged
	}
	select {
	case <-changed:
		return true
	case <-timedOut:
		return false
	}
}

// pumpMainThread 最多处理一个队首主线程任务；没有任务时 Gosched。
// 它绝不推进逻辑帧、时间或脚本轮次。
func (p *Coroutines) pumpMainThread() {
	if job := p.takeMainThreadJob(); job != nil {
		p.runMainThreadJob(job)
	} else {
		runtime.Gosched()
	}
}

// runMainThreadJob 在 Update 外服务引擎调用，并跳过调用方已经取消的任务。
func (p *Coroutines) runMainThreadJob(job *WaitJob) {
	if job == nil || (job.Th != nil && p.isThreadCanceled(job.Th)) {
		return
	}
	job.Call()
}

// takeMainThreadJob 只取队首的 waitTypeMainThread，绝不越过普通等待任务改变调度顺序。
func (p *Coroutines) takeMainThreadJob() *WaitJob {
	p.schedulerMu.Lock()
	defer p.schedulerMu.Unlock()
	if job, ok := p.currentJobs.PeekFront(); ok && job.Type == waitTypeMainThread {
		return p.currentJobs.PopFront()
	}
	return nil
}
