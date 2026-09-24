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
	"context"
	"time"

	"github.com/goplus/spbase/mathf"
	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine"
)

// OnEngineStart 是一局 Go 游戏的“一次性启动入口”。
// 最近调用方：internal/engine.onStart() -> binding.callbacks.OnEngineStart()。
// Web 最顶层来源：普通模式 LinkSession.Run() 补发 OnEngineStart，或真实 Godot 启动回调。
// 进入本函数前，PrepareLink() 已经完成 Web FFI、Go Manager 和回调表绑定。
//
// sync.Once 保证同一局生命周期只进入一次。真正的项目加载和 bootstrap
// 不在回调栈中同步完成，而是通过 engine.Go 放入 Go 引擎调度器，避免阻塞
// Godot 的回调线程；reset() 会重置 RunOnce，下一局可以重新进入。
func (p *Game) OnEngineStart() {
	// 一次性保护：Godot 可能重复发送启动通知，但一局游戏只能初始化一次。
	p.lifecycleState.RunOnce.Do(func() {
		// 每局重新建立边界缓存，避免沿用上一局的碰撞/感知结果。
		cachedBounds = make(map[string]mathf.Rect2) //全局变量
		// 保存本次 bootstrap 的代际编号。发生 reset 时编号会递增，旧任务
		// 可据此识别自己已经过期，避免旧一局继续修改新一局状态。
		generation := p.bootstrapGeneration()
		// 将启动工作交给 Go 引擎调度器；这里通常只负责排队，不在当前
		// Godot 回调栈中直接执行完整的项目加载。
		engine.Go(p, func(context.Context) {
			// reset/reload 期间如果代际已经变化，丢弃这次过期启动任务。
			if !p.isCurrentBootstrap(generation) {
				return
			}
			// 生成的项目 Gamer 如果提供 MainEntry，则先登记项目级 main。
			// 这里只排队，实际执行由 startBootstrap() 统一按顺序处理。
			if me, ok := p.gamer.(interface{ MainEntry() }); ok {
				p.queueBootstrap(generation, func() {
					runMainUntilYield(p, me.MainEntry)
				})
			}
			// 第一次启动时加载项目配置、舞台、精灵和系统；同一局重新
			// 进入时如果已经运行，则避免重复加载整套项目资源。
			if !p.lifecycleState.IsRunned.Load() {
				if err := p.loadGame("assets", generation); err != nil {
					engine.Panic(err)
					return
				}
			}
			// 只有当前代际仍然有效，才把游戏标记为可接受正常帧更新。
			if !p.markGameStarted(generation) {
				return
			}
			// 再启动第二个调度任务，依次执行 MainEntry、碰撞初始化、
			// 精灵 awake/Main 和 OnLoaded 等 bootstrap 阶段任务。
			p.startBootstrap(generation)
		})
	})
}

func (p *Game) OnEngineDestroy() {
	p.resetBootstrap()
	p.discardPenCommands()
	p.abortInputSession("game destroyed")
}

func (p *Game) OnEngineReset() {
	p.reset()
}

// OnEngineBeforeUpdate samples input and conditions before the clock advances.
func (p *Game) OnEngineBeforeUpdate(delta float64) {
	p.scriptEvents.pendingConditions = nil
	if p.lifecycleState.IsRunned.Load() {
		if session := p.currentInputSession(); session != nil && !p.inputMgr.prepareInputSessionTick(session, delta) {
			return
		}
	}
	if p.lifecycleState.StartDispatched.Load() {
		p.scriptEvents.sampleConditions()
	}
}

func (p *Game) OnEngineUpdate(float64) {
	if !p.lifecycleState.IsRunned.Load() {
		return
	}
	session := p.currentInputSession()
	if session != nil && session.input.pending == nil {
		return
	}
	p.scriptEvents.dispatchConditions()
	if session != nil {
		p.inputMgr.dispatchInputSessionTick(session)
	}
	p.soundMgr.Update()
	p.runFrameScripts()
	p.updateSpriteProxies()
	p.pullPhysicsPositions()
}

func (p *Game) OnEngineRender(float64) {
	defer p.flushPenCommands()
	if !p.lifecycleState.IsRunned.Load() {
		return
	}
	// Flush coroutine changes before drawing.
	p.shapeMgr.takeCloneProxyPublications()
	p.syncPostCoroutineVisuals()
	// Drain bootstrap collisions before OnStart.
	p.processPhysicsTriggers()
}

// OnEngineFrameEnd pauses completed replays after rendering and capture.
func (p *Game) OnEngineFrameEnd() {
	p.finishInputSessionFrame()
}

func (p *Game) OnEnginePause(bool) {
	// Pause is handled by engine managers.
}

func (p *Game) runFrameScripts() {
	if p.lifecycleState.BootstrapDone.Load() && !p.lifecycleState.StartDispatched.Load() {
		p.dispatchStartEventIfNeeded()
		return
	}
	engine.RunFrameCallbacks()
}

// runMainUntilYield 在协程中运行一个项目入口，直到入口主动 yield 或执行结束。
//
// 调用方：OnEngineStart() 排入的 Gamer.MainEntry，以及 runSpriteMainsUntilYield()。
// 这样可以让 main 脚本先执行初始化代码，同时把后续等待交还给逐帧调度器。
func runMainUntilYield(owner coroutine.ThreadObj, mainFn func()) {
	thread := gco.Create(owner, func(coroutine.Thread) {
		runMain(mainFn)
	})
	gco.JoinYieldedOrDone(thread)
}

func runSpriteMainsUntilYield(inits []Sprite) {
	// Preserve load/Z-order while letting each Main run until its first yield.
	for _, ini := range inits {
		spr := spriteOf(ini)
		if spr == nil {
			continue
		}

		runMainUntilYield(spr.owner, ini.Main)
	}
}

// queueBootstrap 把一个 bootstrap 阶段任务按登记顺序放入待执行队列。
// generation 用于拒绝 reset 后仍在运行的旧任务；call 为 nil 时不入队。
func (p *Game) queueBootstrap(generation uint64, call func()) bool {
	if call == nil {
		return false
	}
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return false
	}
	p.pendingBootstrap = append(p.pendingBootstrap, call)
	return true
}

// bootstrapGeneration 返回当前项目生命周期的代际编号。
// 每次 resetBootstrap() 都会递增该编号，用来隔离旧一局的异步任务。
func (p *Game) bootstrapGeneration() uint64 {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	return p.bootstrapGen
}

func (p *Game) resetBootstrap() {
	p.bootstrapMu.Lock()
	p.bootstrapGen++
	p.bootstrapStarted = false
	p.startScheduled = false
	p.pendingBootstrap = nil
	p.lifecycleState.IsRunned.Store(false)
	p.lifecycleState.BootstrapDone.Store(false)
	p.lifecycleState.StartDispatched.Store(false)
	p.bootstrapMu.Unlock()
}

// runBootstrapTasks 执行当前代际的全部 bootstrap 任务。
// 任务本身也可以继续 queueBootstrap，因此每轮取出一批，直到队列为空。
func (p *Game) runBootstrapTasks(generation uint64) {
	for {
		tasks := p.takeBootstrapTasks(generation)
		if len(tasks) == 0 {
			return
		}
		// Also drain tasks queued by earlier tasks.
		for _, task := range tasks {
			if !p.isCurrentBootstrap(generation) {
				return
			}
			task()
		}
	}
}

// takeBootstrapTasks 在锁保护下取出当前代际的任务，并立即清空待执行列表。
// 任务执行期间新加入的任务会留到下一轮，避免持锁执行用户脚本。
func (p *Game) takeBootstrapTasks(generation uint64) []func() {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return nil
	}
	tasks := p.pendingBootstrap
	p.pendingBootstrap = nil
	return tasks
}

// isCurrentBootstrap 判断任务是否仍属于当前游戏生命周期。
// 这是异步 bootstrap 任务防止“旧局写入新局”的统一检查点。
func (p *Game) isCurrentBootstrap(generation uint64) bool {
	return generation == p.bootstrapGeneration()
}

// completeBootstrap 标记当前代际的项目 bootstrap 已完成。
// OnEngineUpdate() 会依据 BootstrapDone 决定何时派发项目 Start 事件。
func (p *Game) completeBootstrap(generation uint64) bool {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return false
	}
	p.lifecycleState.BootstrapDone.Store(true)
	return true
}

// markGameStarted 把当前代际标记为可运行状态。
// 它发生在 loadGame() 完成后，但早于 bootstrap 队列全部执行完成；
// 因此 IsRunned 与 BootstrapDone 表示两个不同阶段。
func (p *Game) markGameStarted(generation uint64) bool {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return false
	}
	p.lifecycleState.IsRunned.Store(true)
	return true
}

func (p *Game) scheduleStartEvent() *eventStart {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if !p.lifecycleState.BootstrapDone.Load() || p.lifecycleState.StartDispatched.Load() || p.startScheduled {
		return nil
	}
	p.startScheduled = true
	return &eventStart{generation: p.bootstrapGen}
}

func (p *Game) takeStartSinks(generation uint64) ([]eventSink, bool) {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return nil, false
	}
	return p.scriptEvents.manager.SnapshotStartOnce(), true
}

func (p *Game) markStartDispatched(generation uint64) bool {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return false
	}
	p.lifecycleState.StartDispatched.Store(true)
	return true
}

// claimBootstrap 抢占当前代际的 bootstrap 执行权。
// 即使多个调度路径尝试启动 bootstrap，也只允许第一个成功。
func (p *Game) claimBootstrap(generation uint64) bool {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen || p.bootstrapStarted {
		return false
	}
	p.bootstrapStarted = true
	return true
}

// startBootstrap 启动 bootstrap 队列的唯一执行任务。
// currentGame 和 claimBootstrap 共同保证当前 Game 且只启动一次。
func (p *Game) startBootstrap(generation uint64) {
	engine.Go(p, func(context.Context) {
		if currentGame() != p || !p.claimBootstrap(generation) {
			return
		}
		p.runBootstrapTasks(generation)
		p.completeBootstrap(generation)
	})
}

func runMain(call func()) {
	// Main 可能由普通 Go 调用，也可能由 SPX 协程调度。
	// 普通调用没有协程线程上下文，直接执行即可。
	if gco == nil || !gco.IsInCoroutine() {
		call()
		return
	}
	// 当前协程承载这个 Main；BeginMain 会记录本次 Main 的开始时间。
	thread := gco.Current()
	// end 是恢复函数：退出本次 Main 时恢复调用前的开始时间（支持嵌套 Main）。
	end := thread.BeginMain(time.Now())
	defer end()
	// 真正执行项目 Main。Main 中的 Wait/Yield 仍由协程调度器处理。
	call()
}
