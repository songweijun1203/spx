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

package engine

import (
	"context"

	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine/profiler"
)

var gco *coroutine.Coroutines

// SetGame installs a non-runtime owner binding and cannot replace Main.
func SetGame(game any) {
	bindingMu.Lock()
	defer bindingMu.Unlock()
	if binding := activeGame.Load(); binding != nil && binding.callbacks != nil {
		panic("engine: SetGame cannot replace an active runtime")
	}
	if game == nil {
		activeGame.Store(nil)
		return
	}
	binding := &gameBinding{owner: game}
	binding.storePhase(gameRunning)
	activeGame.Store(binding)
}

func GetGame() any {
	if binding := activeGame.Load(); binding != nil {
		return binding.owner
	}
	return nil
}

func IsInCoroutine() bool {
	return gco != nil && gco.IsInCoroutine()
}

func GetCoroutineOwner() any {
	if thread := currentThread(); thread != nil {
		return thread.Obj
	}
	return nil
}

func GetCurrentThreadContext() context.Context {
	if thread := currentThread(); thread != nil {
		return thread.Context()
	}
	return context.Background()
}

func SetCoroutines(co *coroutine.Coroutines) {
	gco = co
	profiler.SetGco(co)
}

// Go 把一项属于当前游戏生命周期的工作创建为受 SPX 调度器管理的 Thread。
//
// 与直接 gco.Create 相比，它先捕获当前 gameBinding，并在 Thread 真正取得 runMu 后
// 再确认绑定仍是当前一局。启动加载协程和 bootstrap 协程都通过这里创建，因此 reset
// 即使发生在“创建之后、开始执行之前”，旧任务也不会写入新游戏。此函数只负责创建，
// 不等待任务结束；Thread 可在 Create 后立即竞争 runMu，不必等到下一次 gco.Update。
func Go(tobj coroutine.ThreadObj, fn func(ctx context.Context)) {
	binding, ok := captureRuntimeWork()
	if !ok {
		return
	}
	gco.Create(tobj, func(me coroutine.Thread) {
		if isRuntimeWorkCurrent(binding) {
			fn(me.Context())
		}
	})
}

func ResolveCoroutineOwner(owner any) any {
	if owner != nil {
		return owner
	}
	if thread := currentThread(); thread != nil {
		return thread.Obj
	}
	return GetGame()
}

func GoWithOwner(owner any, fn func(ctx context.Context, owner any)) {
	owner = ResolveCoroutineOwner(owner)
	Go(owner, func(ctx context.Context) {
		fn(ctx, owner)
	})
}

func currentThread() coroutine.Thread {
	co := gco
	if co == nil || !co.IsInCoroutine() {
		return nil
	}
	return co.Current()
}
