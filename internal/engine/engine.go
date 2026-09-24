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
	"errors"
	"sync"
	"sync/atomic"

	"github.com/goplus/spx/v3/internal/engine/profiler"
	"github.com/goplus/spx/v3/internal/enginewrap"
	gde "github.com/goplus/spx/v3/internal/gdengine"
	itime "github.com/goplus/spx/v3/internal/time"

	gdx "github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

type (
	Object = gdx.Object
	Array  = gdx.Array
)

const Float2IntFactor = gdx.Float2IntFactor

var (
	activeGame atomic.Pointer[gameBinding]
	bindingMu  sync.Mutex
	updateMu   sync.Mutex
	updateBusy atomic.Bool
	logicMu    sync.Mutex
)

var ErrGameAlreadyRunning = errors.New("spx: a game is already running")

type gamePhase uint32

const (
	gameStarting gamePhase = iota
	gameRunning
	gameReloading
	gameStopped
	gameClosing
)

type gameBinding struct {
	callbacks IGame
	owner     any
	phase     atomic.Uint32
	startDone chan struct{}
	// link 持有本次游戏与 Godot/平台后端的绑定会话；退出或重置时必须由同一对象清理。
	link                 *gde.LinkSession
	resetReleaseDeferred atomic.Bool
	destroyReady         atomic.Bool
	backendDestroyed     atomic.Bool
}

type IGame interface {
	OnEngineStart()
	OnEngineUpdate(delta float64)
	OnEngineBeforeUpdate(delta float64)
	OnEngineRender(delta float64)
	OnEngineFrameEnd()
	OnEngineDestroy()
	OnEngineReset()
	OnEnginePause(isPaused bool)
}

func ConvertToFloat64(value int64) float64 {
	return float64(value) / Float2IntFactor
}

func ConvertToInt64(value float64) int64 {
	return int64(value * Float2IntFactor)
}

func Lock() {
	logicMu.Lock()
}

func Unlock() {
	logicMu.Unlock()
}

// Main 持有一局游戏从初始化、绑定到后端清理的完整生命周期。
// 最近调用方：spx.XGot_Game_Main()，它是编译/解释后的 XGo 项目入口。
// Web 最顶层入口：GameApp.StartGame() -> ispx_start() -> ispx.Run() -> 游戏 main.go。
// Native 最顶层入口：Godot 加载 GDExtension 后调用最终程序 main.main。
func Main(game IGame, owner any, initialize func()) error {
	binding, err := bindGameAtPhase(game, owner, gameStarting)
	if err != nil {
		return err
	}
	committed := false
	finishStart := sync.OnceFunc(func() { close(binding.startDone) })
	defer func() {
		finishStart()
		if !committed {
			abortGameStart(binding)
		}
	}()

	if initialize != nil {
		initialize()
	}
	enginewrap.Init(WaitMainThread)
	callbacks := gdx.CoreCallbackInfo{
		OnEngineStart:     onStart,
		OnEngineUpdate:    onUpdate,
		OnEngineDestroy:   onDestroy,
		OnEngineDestroyed: onDestroyed,
		OnEngineReset:     onReset,
		OnEnginePause:     onPause,
		OnMousePressed:    onMousePressed,
		OnMouseReleased:   onMouseReleased,
		OnKeyPressed:      onKeyPressed,
		OnKeyReleased:     onKeyReleased,
	}
	if !activateGame(binding, func() {
		binding.link = gde.PrepareLink(callbacks)
	}) {
		return nil
	}
	binding.link.Run(finishStart)
	committed = true
	return nil
}

// 接收 gdengine 转发的 OnEngineStart，并继续调用具体 Game.OnEngineStart()。
// 最近调用方：gdengine.onEngineStart() 保存于 PrepareLink 的 coreCallbacks。
// Web 最顶层来源：普通模式 LinkSession.Run() 补发事件，或 Godot -> JS -> Go 的真实启动回调。
func onStart() {
	defer CheckPanic()
	binding := runningBinding()
	if binding == nil {
		return
	}
	ResetInputState()
	resetTriggerEvents()

	itime.Start(Managers().PlatformMgr.SetTimeScale)
	binding.callbacks.OnEngineStart()
}

// onUpdate 是 Go 侧一帧游戏流程的总调度入口。
//
// 最近调用方：internal/gdengine.onEngineUpdate() 的 coreCallbacks.OnEngineUpdate；
// 最顶层来源：Godot 每帧 -> Native/Web FFI -> gdengine.onEngineUpdate()。
//
// 一帧的处理顺序是：缓存输入和碰撞事件 -> 游戏更新 -> 协程恢复 ->
// 渲染前同步 -> 截图提交 -> 帧结束处理。Game.OnEngineRender 虽然是
// “渲染阶段”回调，但它仍然由本函数在 GameUpdate 和帧结束之间调用，
// 并不是另一条独立的 Godot 引擎入口。
func onUpdate(delta float64) {
	// 统一把本帧回调中的 panic 转为引擎运行时错误处理。
	defer CheckPanic()
	// 所有更新阶段共享这把锁，避免同一局游戏被重入执行两次。
	updateMu.Lock()
	defer updateMu.Unlock()
	// 标记当前正在处理帧更新；销毁、重置等路径据此等待或拒绝并发操作。
	updateBusy.Store(true)
	defer updateBusy.Store(false)
	// 获取当前仍处于 gameRunning 状态的游戏绑定；游戏已销毁时直接丢弃帧事件。
	binding := runningBinding()
	if binding == nil {
		return
	}
	// profiler 以一帧为样本，记录本帧总耗时和下面几个阶段的耗时。
	profiler.BeginSample()
	defer profiler.EndSample()
	// 把跨线程/跨回调到达的待处理碰撞、键盘和鼠标事件转移到本帧的 ready 队列。
	cacheTriggerEvents()
	cacheKeyEvents()
	cacheMouseEvents()
	// 在游戏时间推进前采样输入条件，并准备输入回放当前 tick。
	binding.callbacks.OnEngineBeforeUpdate(delta)
	// 输入回调可能触发 reset/destroy；绑定失效时不要继续推进旧游戏。
	if !binding.isCurrent(gameRunning) {
		return
	}
	// 推进 SPX 逻辑时间；Calcfps 同时计算用于调试和时间缩放的近似 FPS。
	itime.Update(delta, profiler.Calcfps())
	// 游戏层更新：输入/声音、帧脚本、精灵代理同步和物理位置同步。
	profiler.MeasureFunctionTime("GameUpdate", func() {
		binding.callbacks.OnEngineUpdate(delta)
	})
	// GameUpdate 中可能主动停止或重载游戏，因此每个阶段后都重新确认绑定状态。
	if !binding.isCurrent(gameRunning) {
		return
	}
	// 恢复本帧到期的 Go 协程，包括 Wait、WaitNextFrame、事件和精灵 Main。
	profiler.MeasureFunctionTime("CoroUpdateJobs", gco.Update)
	// 协程恢复期间也可能触发游戏切换，旧绑定不能继续进入渲染阶段。
	if !binding.isCurrent(gameRunning) {
		return
	}
	// 渲染准备阶段：提交协程产生的视觉变化，处理触发结果并刷新画笔同步缓存。
	profiler.MeasureFunctionTime("GameRender", func() {
		binding.callbacks.OnEngineRender(delta)
	})
	// 渲染同步可能触发销毁或重置；失效时跳过截图和帧结束回调。
	if !binding.isCurrent(gameRunning) {
		return
	}
	// 分发本帧排队的截图/捕获请求；必须在渲染准备完成后执行。
	if err := FlushCaptures(); err != nil {
		Panic(err)
		return
	}
	// 截图处理也可能改变生命周期，提交帧结束通知前再次确认当前绑定。
	if !binding.isCurrent(gameRunning) {
		return
	}
	// 通知输入回放等帧末逻辑，本帧正式结束。
	binding.callbacks.OnEngineFrameEnd()
}

// 最近调用方：gdengine.onEngineDestroy()；最顶层来源：Godot/SpxEngine 销毁流程。
func onDestroy() {
	defer CheckPanic()
	binding := activeGame.Load()
	if binding == nil || binding.callbacks == nil || !beginGameClose(binding) {
		return
	}
	waitForGameStart(binding)
	drainCoroutines(func() {
		defer func() {
			ResetFrameRuntime()
			binding.destroyReady.Store(true)
			releaseDestroyedGame(binding)
		}()
		binding.callbacks.OnEngineDestroy()
	})
}

// onDestroyed releases the binding after backend teardown.
func onDestroyed() {
	defer CheckPanic()
	binding := activeGame.Load()
	if binding == nil {
		return
	}
	binding.backendDestroyed.Store(true)
	releaseDestroyedGame(binding)
}

func onPause(paused bool) {
	defer CheckPanic()
	binding := runningBinding()
	if binding == nil {
		return
	}
	binding.callbacks.OnEnginePause(paused)
}

// 最近调用方：gdengine.onEngineReset()；最顶层入口：Web 停止本局游戏触发 C++ runtime reset。
func onReset() {
	defer CheckPanic()
	binding := activeGame.Load()
	if binding == nil || binding.callbacks == nil {
		return
	}
	ownsRelease := beginGameClose(binding)
	if !ownsRelease && !binding.resetReleaseDeferred.Load() {
		return
	}
	waitForGameStart(binding)
	if !ownsRelease {
		binding.callbacks.OnEngineReset()
		return
	}

	drainCoroutines(func() {
		defer releaseGameAfter(binding, binding.unlink)
		binding.callbacks.OnEngineReset()
	})
}

// Keep cleanup on the caller thread and finish before backend teardown.
func drainCoroutines(cleanup func()) {
	if gco == nil {
		cleanup()
		return
	}
	gco.RunAfterStopAll(0, cleanup)
}

func (b *gameBinding) loadPhase() gamePhase {
	return gamePhase(b.phase.Load())
}

func (b *gameBinding) storePhase(phase gamePhase) {
	b.phase.Store(uint32(phase))
}

func (b *gameBinding) isCurrent(phase gamePhase) bool {
	return activeGame.Load() == b && b.loadPhase() == phase
}

func (b *gameBinding) transition(from, to gamePhase) bool {
	bindingMu.Lock()
	defer bindingMu.Unlock()
	if !b.isCurrent(from) {
		return false
	}
	b.storePhase(to)
	return true
}

func runningBinding() *gameBinding {
	binding := activeGame.Load()
	if binding == nil || binding.callbacks == nil || binding.loadPhase() != gameRunning {
		return nil
	}
	return binding
}

func acceptsRuntimeWork() bool {
	_, accepted := captureRuntimeWork()
	return accepted
}

func captureRuntimeWork() (*gameBinding, bool) {
	binding := activeGame.Load()
	return binding, binding == nil || binding.loadPhase() == gameRunning
}

func isRuntimeWorkCurrent(binding *gameBinding) bool {
	current := activeGame.Load()
	return current == binding && (binding == nil || binding.loadPhase() == gameRunning)
}

func beginGameClose(binding *gameBinding) bool {
	bindingMu.Lock()
	defer bindingMu.Unlock()
	if binding == nil || activeGame.Load() != binding || binding.loadPhase() == gameClosing {
		return false
	}
	binding.storePhase(gameClosing)
	return true
}

func beginDeferredReset() (*gameBinding, bool) {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	binding := activeGame.Load()
	if binding != nil && binding.loadPhase() == gameClosing {
		return binding, false
	}
	if binding == nil {
		binding = &gameBinding{}
	}
	binding.storePhase(gameClosing)
	binding.resetReleaseDeferred.Store(true)
	activeGame.Store(binding)
	return binding, true
}

func finishDeferredReset(binding *gameBinding) {
	waitForGameStart(binding)
	releaseGameAfter(binding, binding.unlink)
}

func (b *gameBinding) unlink() {
	if b != nil && b.link != nil {
		b.link.Unlink()
	}
}

func bindGameAtPhase(callbacks IGame, owner any, phase gamePhase) (*gameBinding, error) {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	if activeGame.Load() != nil {
		return nil, ErrGameAlreadyRunning
	}
	binding := &gameBinding{callbacks: callbacks, owner: owner}
	binding.storePhase(phase)
	if phase == gameStarting {
		binding.startDone = make(chan struct{})
	}
	activeGame.Store(binding)
	return binding, nil
}

func activateGame(binding *gameBinding, prepare func()) bool {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	if !binding.isCurrent(gameStarting) {
		return false
	}
	prepare()
	binding.storePhase(gameRunning)
	return true
}

func abortGameStart(binding *gameBinding) {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	if activeGame.Load() != binding || binding.loadPhase() == gameClosing {
		return
	}
	binding.unlink()
	activeGame.Store(nil)
}

func waitForGameStart(binding *gameBinding) {
	if binding != nil && binding.startDone != nil {
		<-binding.startDone
	}
}

func releaseDestroyedGame(binding *gameBinding) bool {
	if binding == nil || !binding.destroyReady.Load() || !binding.backendDestroyed.Load() {
		return false
	}
	return releaseGameAfter(binding, binding.unlink)
}

// releaseGameAfter clears the binding after teardown.
func releaseGameAfter(binding *gameBinding, teardown func()) bool {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	if activeGame.Load() != binding {
		return false
	}
	if teardown != nil {
		teardown()
	}
	activeGame.Store(nil)
	return true
}
