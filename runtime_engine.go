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
	"time"

	"github.com/goplus/spx/v3/internal/coroutine"

	"github.com/goplus/spbase/mathf"
	coreruntime "github.com/goplus/spx/v3/internal/core/runtime"
	"github.com/goplus/spx/v3/internal/engine"
	spxlog "github.com/goplus/spx/v3/internal/log"
)

// -----------------------------------------------------------------------------
// Engine Callbacks
// -----------------------------------------------------------------------------
func (p *Game) OnEngineStart() {
	p.lifecycleState.RunOnce.Do(func() {
		cachedBounds = make(map[string]mathf.Rect2)
		generation := p.currentBootstrapGeneration()
		go func() {
			defer engine.CheckPanic()
			//MainEntry 注册 main.spx 中的舞台事件
			if me, ok := p.gamer.(interface{ MainEntry() }); ok {
				p.deferBootstrapFor(generation, func() {
					p.runBootstrapMainUntilYield(p, me.MainEntry)
				})
			}
			if !p.lifecycleState.IsRunned.Load() {
				builder := newGameBuilder(p.gamer, "assets", generation)
				if err := builder.buildAndRun(); err != nil {
					engine.Panic(err)
					return
				}
			}
			engine.OnGameStarted()
			p.lifecycleState.IsRunned.Store(true)
			p.startBootstrapPhaseFor(generation)
		}()
	})
}

func (p *Game) OnEngineDestroy() {
	p.lifecycleState.IsRunned.Store(false)
	p.discardPenCommands()
	p.abortInputSession("game destroyed")
}

func (p *Game) OnEngineReset() {
	p.reset()
}

// OnEngineBeforeUpdate resolves input and samples conditions before advancing the clock.
// 在本帧逻辑时间推进前确定本帧有效输入，然后对OnCond条件帽进行一次一致性采样。它不直接执行条件对应的脚本
func (p *Game) OnEngineBeforeUpdate(delta float64) {
	//pendingConditions 上一次条件采样中，已经判断为满足触发条件、等待派发的 OnCond 处理器
	//这里不是清空键盘 鼠标 碰撞事件
	//正常情况下，本帧采样得到的pendingConditions会在 OnEngineUpdate 中消费并清空，这里再次设为nil是边界保护
	//避免异常路径或者上一帧未完成派发时留下旧结果
	p.scriptEvents.pendingConditions = nil
	if p.lifecycleState.IsRunned.Load() { //判断游戏是否已经运行
		if session := p.currentInputSession(); session != nil {
			if !p.inputMgr.prepareInputSessionTick(session, delta) {
				return
			}
		}
	}
	if p.lifecycleState.StartDispatched.Load() {
		//对所有 OnCond 条件进行采样
		p.scriptEvents.sampleConditions()
	}
}

// 1 游戏帧的逻辑准备
// 2 分发条件事件
// 3 分发处理录制/回放输入
// 4 更新声音状态
// 5 触发开始事件或帧回调
// 6 把已有 Go 精灵状态同步给 C++
// 7 读取物理精灵位置
func (p *Game) OnEngineUpdate(delta float64) {
	if !p.lifecycleState.IsRunned.Load() {
		return
	}
	session := p.currentInputSession()
	if session != nil && session.input.pending == nil {
		//如果是录制/回放输入模式，且本帧没有任何输入事件，则不执行游戏逻辑
		//因为游戏逻辑的执行会消耗CPU资源，且在录制/回放输入模式下，游戏逻辑的执行结果是不可控的
		//所以在没有任何输入事件的情况下，不执行游戏逻辑，可以节省CPU资源
		return
	}

	//派发条件事件，执行满足条件的 OnCond 处理器
	p.scriptEvents.dispatchConditions()
	if session != nil {
		p.inputMgr.dispatchInputSessionTick(session)
	}
	p.soundMgr.Update()
	p.runScriptFramePhase()
	p.updateSpriteProxies()
	p.pullPhysicsPositions()
}

func (p *Game) OnEngineRender(delta float64) {
	// 游戏脚本协程在 OnEngineUpdate 与这里之间运行。鼠标绘图脚本在协程中
	// 调用 SetXYpos/PenDown/PenUp 后，defer 会在本帧收尾时按原顺序一次性
	// 把命令交给 C++；即使下面因游戏尚未运行而提前返回，也不会漏掉队列。
	defer p.flushPenCommands()
	if !p.lifecycleState.IsRunned.Load() {
		return
	}
	// Coroutines run between OnEngineUpdate and OnEngineRender. Flush again when
	// a clone finished its first slice or a capture/replay needs post-coroutine
	// visual state, without advancing ordinary shape logic a second time.
	if p.shapeMgr.takeCloneProxyPublications() || engine.HasPendingCaptures() ||
		p.inputSessionFrameCompletionPending() {
		p.syncPostCoroutineVisuals()
	}
	// Initial sprite Main hooks can move and collide during bootstrap, so
	// trigger pairs must be drained before the start event is dispatched.
	p.processPhysicsTriggers()
}

// OnEngineFrameEnd runs after the current update's coroutine work, render-side
// proxy synchronization, and capture dispatch have completed. Replays pause at
// this boundary so their final effective input frame is fully observable before
// any later update can run.
func (p *Game) OnEngineFrameEnd() {
	p.finishInputSessionFrame()
}

func (p *Game) OnEnginePause(bool) {
	// Pause lifecycle hooks are intentionally handled by engine-level managers.
}

// runScriptFramePhase dispatches either the initial start event or due frame callbacks.
func (p *Game) runScriptFramePhase() {
	if p.lifecycleState.BootstrapDone.Load() && !p.lifecycleState.StartDispatched.Load() {
		p.dispatchStartEventIfNeeded()
		return
	}
	engine.RunFrameCallbacks()
}

// -----------------------------------------------------------------------------
// Loop Setup
// -----------------------------------------------------------------------------
func (p *Game) runLoop(cfg *Config) (err error) {
	spxlog.Debug("RunLoop")
	if !cfg.DontRunOnUnfocused {
		p.engine().PlatformMgr.SetRunnableOnUnfocused(true)
	}
	p.initEventLoop()
	p.engine().PlatformMgr.SetWindowTitle(cfg.Title)
	return nil
}

func (p *Game) runBootstrapMainUntilYield(owner coroutine.ThreadObj, mainFn func()) {
	if mainFn == nil {
		return
	}

	thread := gco.CreateAndStart(false, owner, func(coroutine.Thread) int {
		runMain(mainFn)
		return 0
	})
	gco.JoinYieldedOrDone(thread)
}

// 调用SpriteMain 完成精灵事件注册
func (p *Game) runBootstrapSpriteMainsUntilYield(inits []Sprite) {
	if len(inits) == 0 {
		return
	}

	// Advance initial sprite Mains in load/Z-order until each reaches its first
	// yield (or return) before continuing bootstrap. This preserves deterministic
	// pre-yield setup while still releasing startup once a long-running Main
	// yields for the first time.
	for _, ini := range inits {
		spr := spriteOf(ini)
		if spr == nil {
			continue
		}

		p.runBootstrapMainUntilYield(spr.pthis, ini.Main) //ini.Main 此处就是入口
	}
}

func (p *Game) deferBootstrap(call func()) {
	p.deferBootstrapFor(p.currentBootstrapGeneration(), call)
}

func (p *Game) deferBootstrapFor(generation uint64, call func()) bool {
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

func (p *Game) currentBootstrapGeneration() uint64 {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	return p.bootstrapGen
}

func (p *Game) resetBootstrapState() {
	p.bootstrapMu.Lock()
	p.bootstrapGen++
	p.bootstrapStarted = false
	p.startScheduled = false
	p.pendingBootstrap = nil
	p.lifecycleState.BootstrapDone.Store(false)
	p.lifecycleState.StartDispatched.Store(false)
	p.bootstrapMu.Unlock()
}

func (p *Game) runBootstrapTasks() {
	p.runBootstrapTasksFor(p.currentBootstrapGeneration())
}

func (p *Game) runBootstrapTasksFor(generation uint64) {
	for {
		tasks, ok := p.takeBootstrapTasksFor(generation)
		if !ok || len(tasks) == 0 {
			return
		}
		// Re-check after each pass so tasks queued by tasks are also consumed.
		for _, task := range tasks {
			if !p.isBootstrapGenerationCurrent(generation) {
				return
			}
			task()
		}
	}
}

func (p *Game) takeBootstrapTasksFor(generation uint64) ([]func(), bool) {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return nil, false
	}
	if len(p.pendingBootstrap) == 0 {
		return nil, true
	}
	tasks := p.pendingBootstrap
	p.pendingBootstrap = nil
	return tasks, true
}

func (p *Game) isBootstrapGenerationCurrent(generation uint64) bool {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	return generation == p.bootstrapGen
}

func (p *Game) markBootstrapDoneFor(generation uint64) bool {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return false
	}
	p.lifecycleState.BootstrapDone.Store(true)
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

func (p *Game) takeStartSinksFor(generation uint64) ([]eventSink, bool) {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return nil, false
	}
	return p.scriptEvents.manager.SnapshotStartOnce(), true
}

func (p *Game) markStartDispatchedFor(generation uint64) bool {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen {
		return false
	}
	p.lifecycleState.StartDispatched.Store(true)
	return true
}

func (p *Game) claimBootstrapPhaseFor(generation uint64) (hasTasks, ok bool) {
	p.bootstrapMu.Lock()
	defer p.bootstrapMu.Unlock()
	if generation != p.bootstrapGen || p.bootstrapStarted {
		return false, false
	}
	p.bootstrapStarted = true
	return len(p.pendingBootstrap) > 0, true
}

func (p *Game) startBootstrapPhaseFor(generation uint64) {
	engine.WaitMainThread(func() {
		hasTasks, ok := p.claimBootstrapPhaseFor(generation)
		if !ok {
			return
		}
		if !hasTasks {
			p.markBootstrapDoneFor(generation)
			return
		}

		gco.CreateAndStart(false, p, func(coroutine.Thread) int {
			p.runBootstrapTasksFor(generation)
			p.markBootstrapDoneFor(generation)
			return 0
		})
	})
}

func runMain(call func()) {
	coreruntime.RunMain(call, time.Now(), setSchedInMain, setMainSchedTime)
}
