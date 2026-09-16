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

// Join 等待 target 永久结束。
//
// 如果调用者是 p 管理的 Thread，本方法会把调用者登记为 target 的等待者，
// 再通过 Yield 释放 runMu；如果调用者不是受管理 Thread，则直接阻塞当前 Go
// goroutine，直到 target.done 被关闭。
func (p *Coroutines) Join(target Thread) {
	// Join 等的是 target 永久结束。若 target 是 forever，正常情况下 Join
	// 永远不会因为一次循环让出而返回。
	if target == nil {
		return
	}

	me := p.currentCoroutineThread()
	if me == nil {
		// 外部 goroutine 发起消息分发时，runScriptEventDispatch 用这条路径等待
		// 额外的分发 Thread 关闭 done；外部调用者不参与 Yield/runMu 调度。
		<-target.done
		return
	}
	if me == target {
		return
	}
	// BroadcastAndWait 在受管 Thread 中走这里：把广播发起者登记到接收者的
	// joinWaiters 后 Yield。目标永久结束时 finishThread 会恢复发起者。
	p.registerWaiterAndYield(me, func() bool {
		return target.addJoinWaiter(me)
	})
}

// JoinAll 等待 targets 中每个不为 nil 且互不重复的 Thread 永久结束。
// 重复项只等待一次，输入切片为空时立即返回。
func (p *Coroutines) JoinAll(targets []Thread) {
	// [消息分发 14] 按批次顺序逐个 Join。等待第一个目标期间，其他目标同样
	// 可以被调度并完成；后续 Join 若发现目标已结束会立即返回。
	joinUnique(targets, p.Join)
}

// JoinYieldedOrDone 等待 target 第一次主动让出执行权或永久结束。
//
// 它常用于“启动子 Thread，并确认子 Thread 的初始化代码已经执行”的场景；
// 它不等待 target 在第一次 Yield 之后的剩余脚本。
func (p *Coroutines) JoinYieldedOrDone(target Thread) {
	// 与 Join 不同，它只等待 target 的第一次执行片段结束：target 第一次
	// Wait/Yield，或者不让出直接返回，都会满足条件。后续恢复仍是原 Thread。
	if target == nil {
		return
	}

	me := p.currentCoroutineThread()
	if me == nil {
		<-target.yieldedOrDone
		return
	}
	if me == target {
		return
	}
	p.registerWaiterAndYield(me, func() bool {
		return target.addYieldWaiter(me)
	})
}

// JoinYieldedOrDoneAll 等待 targets 中每个不为 nil 且互不重复的 Thread
// 第一次主动让出执行权或永久结束。
func (p *Coroutines) JoinYieldedOrDoneAll(targets []Thread) {
	joinUnique(targets, p.JoinYieldedOrDone)
}

// joinUnique 对目标 Thread 去重并忽略 nil，再依照输入中的首次出现顺序调用 join。
// join 参数决定具体等待“永久结束”还是“首次让出或结束”。
func joinUnique(targets []Thread, join func(Thread)) {
	if len(targets) == 0 {
		return
	}
	if len(targets) == 1 {
		join(targets[0])
		return
	}

	seen := make(map[Thread]struct{}, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		join(target)
	}
}

// registerWaiterAndYield 原子地发布 me 的阻塞状态和等待者登记，然后让出 runMu。
//
// register 返回 true 表示登记成功，me 必须等待目标日后唤醒；返回 false 表示
// 目标条件已经满足，函数会把 me 恢复为 runnable，并且不执行 Yield。
func (p *Coroutines) registerWaiterAndYield(me Thread, register func() bool) {
	// 必须在同一个 schedulerMu 临界区内发布等待者登记和 blocked 状态。
	// 当前 Thread 作为“等待者”登记到 target 后必须 Yield 释放 runMu，否则
	// target 无法执行到结束/让出，也就永远无法反过来唤醒当前 Thread。
	p.schedulerMu.Lock()
	p.setThreadStateLocked(me, threadBlocked)
	registered := register()
	if !registered {
		p.setThreadStateLocked(me, threadRunnable)
	}
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()

	if registered {
		p.Yield(me)
	}
}

// finishYieldWaiters 只在 th 首次 Yield 或永久结束时生效一次。
// 它关闭 yieldedOrDone，并取走当时登记的全部“首次让出”等待者交给调用方唤醒。
func (th *threadImpl) finishYieldWaiters() (waiters []Thread) {
	th.yieldedOrDoneOnce.Do(func() {
		th.waitersMu.Lock()
		if len(th.yieldWaiters) != 0 {
			waiters = copyThreadSet(th.yieldWaiters)
			th.yieldWaiters = nil
		}
		close(th.yieldedOrDone)
		th.waitersMu.Unlock()
	})
	return waiters
}

// addYieldWaiter 把 waiter 登记为等待 th 首次让出或结束的 Thread。
// 返回 false 表示条件此前已经满足，waiter 无需进入阻塞状态。
func (th *threadImpl) addYieldWaiter(waiter Thread) bool {
	if waiter == nil {
		return false
	}

	th.waitersMu.Lock()
	defer th.waitersMu.Unlock()
	select {
	case <-th.yieldedOrDone:
		return false
	default:
	}
	if th.yieldWaiters == nil {
		th.yieldWaiters = make(map[Thread]struct{})
	}
	th.yieldWaiters[waiter] = struct{}{}
	return true
}

// addJoinWaiter 把 waiter 登记为等待 th 永久结束的 Thread。
// 返回 false 表示 th 已完成 Join 收尾，waiter 无需进入阻塞状态。
func (th *threadImpl) addJoinWaiter(waiter Thread) bool {
	if waiter == nil {
		return false
	}

	th.waitersMu.Lock()
	defer th.waitersMu.Unlock()
	if th.joinDone {
		return false
	}
	if th.joinWaiters == nil {
		th.joinWaiters = make(map[Thread]struct{})
	}
	th.joinWaiters[waiter] = struct{}{}
	return true
}

// finishJoinWaiters 标记 th 已永久结束，并取走当时登记的全部 Join 等待者。
// 调用方负责把返回的 Thread 标记为 runnable 并发送 Resume。
func (th *threadImpl) finishJoinWaiters() []Thread {
	th.waitersMu.Lock()
	waiters := copyThreadSet(th.joinWaiters)
	th.joinWaiters = nil
	th.joinDone = true
	th.waitersMu.Unlock()
	return waiters
}

// copyThreadSet 把用 map 表示的 Thread 集合复制成独立切片。
// 调用方通常借此缩短持锁时间，在释放集合自身的锁之后再逐个唤醒 Thread。
func copyThreadSet(set map[Thread]struct{}) []Thread {
	threads := make([]Thread, 0, len(set))
	for th := range set {
		threads = append(threads, th)
	}
	return threads
}
