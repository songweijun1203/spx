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
	"fmt"
	"sync"
	stime "time"

	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine/platform"
	"github.com/goplus/spx/v3/internal/engine/profiler"
	"github.com/goplus/spx/v3/internal/enginewrap"
	gde "github.com/goplus/spx/v3/internal/gdengine"
	spxlog "github.com/goplus/spx/v3/internal/log"
	"github.com/goplus/spx/v3/internal/time"

	gdx "github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

// Shared engine managers.
var (
	platformMgr enginewrap.PlatformMgrImpl
	resMgr      enginewrap.ResMgrImpl
	extMgr      enginewrap.ExtMgrImpl
)

type Object = gdx.Object
type Array = gdx.Array

type layerSortMode int

const (
	layerSortModeNone layerSortMode = iota
	layerSortModeVertical
)

type LayerSortInfo struct {
	X      float64
	Y      float64
	Sprite *Sprite
}

var curLayerSortMode layerSortMode

// SetLayerSortMode sets sprite layer sorting.
// Supported modes:
//   - "" or "none": disable sorting (default)
//   - "vertical": sort by Y, then X, both descending
//
// When enabled, manual layer changes are disabled.
func SetLayerSortMode(s string) error {
	switch s {
	case "", "none":
		curLayerSortMode = layerSortModeNone
	case "vertical":
		curLayerSortMode = layerSortModeVertical
	default:
		return fmt.Errorf("unknown layer sort mode: %s", s)
	}

	extMgr.SetLayerSorterMode(int64(curLayerSortMode))
	return nil
}

func HasLayerSortMethod() bool {
	return curLayerSortMode != layerSortModeNone
}

const Float2IntFactor = gdx.Float2IntFactor

func ConvertToFloat64(val int64) float64 {
	return float64(val) / Float2IntFactor
}
func ConvertToInt64(val float64) int64 {
	return int64(val * Float2IntFactor)
}

type TriggerEvent struct {
	Src *Sprite
	Dst *Sprite
}

var (
	game              IGame
	triggerEventsTemp []TriggerEvent
	triggerEvents     []TriggerEvent
	triggerMutex      sync.Mutex

	logicMutex sync.Mutex
)

func Lock() {
	logicMutex.Lock()
}

func Unlock() {
	logicMutex.Unlock()
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

func Main(g IGame) {
	enginewrap.Init(WaitMainThread)
	game = g
	gde.Link(gdx.CoreCallbackInfo{
		OnEngineStart:   onStart,
		OnEngineUpdate:  onUpdate,
		OnEngineDestroy: onDestroy,
		OnEngineReset:   onReset,
		OnEnginePause:   onPaused,
		OnMousePressed:  onMousePressed,
		OnMouseReleased: onMouseReleased,
		OnKeyPressed:    onKeyPressed,
		OnKeyReleased:   onKeyReleased,
	})
}

func OnGameStarted() {
	gco.OnInited()
}

// Engine callbacks.
func onStart() {
	defer CheckPanic()
	resetInputState()
	triggerEventsTemp = make([]TriggerEvent, 0)
	triggerEvents = make([]TriggerEvent, 0)

	time.Start(func(scale float64) {
		platformMgr.SetTimeScale(scale)
	})
	game.OnEngineStart()
}

// -----------------------------------------------------------------------------
// 总体调用逻辑
// -----------------------------------------------------------------------------
// 1 缓存 C++ 输入和碰撞事件
// 2 准备输入与条件采样
// 3 推进 SPX 逻辑时钟
// 4 执行游戏帧准备及前置同步
// 5 读取所有游戏脚本协程
// 6 提交协程执行后的视觉状态
// 7 处理截图
// 8 结束录制/回放输入帧
func onUpdate(delta float64) {
	defer CheckPanic()
	profiler.BeginSample()
	defer profiler.EndSample()
	cacheTriggerEvents() //本帧到达的碰撞事件从临时队列缓存到主队列，供游戏逻辑处理，Game.OnEngineRender 的 processPhysicsTriggers() 中消费
	cacheKeyEvents()     //本帧到达的按键事件从临时队列缓存到主队列，供游戏逻辑处理
	cacheMouseEvents()   //本帧到达的鼠标事件从临时队列缓存到主队列，供游戏逻辑处理

	//清空上一帧的事件
	//处理录制/回放输入
	//采样 touching、keyPressed 等条件事件
	//它放在 updateTime 之前，意味着条件采样使用的是时间推进前的本帧输入状态。
	game.OnEngineBeforeUpdate(delta) //位于runtime_engine.go中，主要是处理输入和条件采样

	//推进 SPX 逻辑时钟，会更新
	// DeltaTime，TimeSinceLevelLoad，当前帧号 Frame，FPS，每调用一次，SPX 帧号增加 1
	updateTime(delta)

	profiler.MeasureFunctionTime("GameUpdate", func() {
		//游戏帧的逻辑准备
		//分发条件事件
		//分发处理录制/回放输入
		//更新声音状态
		//触发开始事件或帧回调
		//把已有 Go 精灵状态同步给 C++
		//读取物理精灵位置
		game.OnEngineUpdate(delta) //位于runtime_engine.go中，主要是处理游戏逻辑
	})

	profiler.MeasureFunctionTime("CoroUpdateJobs", func() {
		//推进 SPX 协程调度器，这里才是游戏脚本的主要执行阶段
		//onStart
		//onMsg
		//onKey
		//forever
		//repeat
		//wait
		//waitUntil
		//满足运行条件的协程会被↩恢复执行，直到
		//主动等待、循环边界让出、执行结束、被取消、达到调度时间预算
		gco.Update()
	})

	profiler.MeasureFunctionTime("GameRender", func() {
		//执行协程结束后的渲染准备，并记录耗时为 GameRender
		//名字容易误解，他不是Godot真正执行CPU渲染，而是绘制前的状态收尾，会：
		//1 必要时同步协程刚修改的视觉状态
		//2 处理克隆发布
		//3 处理物理触发事件
		//4 刷新画笔命令
		game.OnEngineRender(delta)
	})

	if err := FlushCaptures(); err != nil {
		Panic(err)
		return
	}

	//标记本帧结束。当前主要用于完成输入录制/回放帧
	//输入已处理 协程已运行 视觉状态已提交 截图已处理
	game.OnEngineFrameEnd()
}

func onDestroy() {
	defer CheckPanic()
	game.OnEngineDestroy()
}

func onPaused(isPaused bool) {
	defer CheckPanic()
	game.OnEnginePause(isPaused)
}

func onReset() {
	defer CheckPanic()
	defer gde.Unlink()
	game.OnEngineReset()
}

func updateTime(delta float64) {
	time.Update(delta, profiler.Calcfps())
}

func cacheTriggerEvents() {
	triggerMutex.Lock()
	triggerEvents = append(triggerEvents, triggerEventsTemp...)
	triggerMutex.Unlock()
	triggerEventsTemp = triggerEventsTemp[:0]
}

func GetTriggerEvents(lst []TriggerEvent) []TriggerEvent {
	triggerMutex.Lock()
	lst = append(lst, triggerEvents...)
	triggerEvents = triggerEvents[:0]
	triggerMutex.Unlock()
	return lst
}

// DeferPanic recovers a panic, reports it, and optionally exits.
func DeferPanic(name, stack string, exitOnPanic bool) {
	if e := recover(); e != nil {
		handlePanic(name, stack, e, exitOnPanic)
	}
}

// CheckPanic is a shorthand panic handler for engine callbacks.
func CheckPanic() {
	if e := recover(); e != nil {
		handlePanic("", "", e, true)
	}
}

// OnPanic reports a coroutine's original panic and its fault and creation stacks.
func OnPanic(report coroutine.PanicReport) {
	stack := report.Stack
	if report.CreationStack != "" {
		stack += "\ncreated at:\n" + report.CreationStack
	}
	handlePanic(report.Name, stack, report.Value, true)
}

// handlePanic reports a panic and optionally exits.
func handlePanic(name, stack string, err any, exitOnPanic bool) {
	var msg string
	if err != nil {
		msg = fmt.Sprintf("panic: %v", err)
	}
	if name != "" {
		if msg != "" {
			msg = name + ": " + msg
		} else {
			msg = name
		}
	}
	if stack != "" {
		msg += "\nstack:\n" + stack
	}

	if msg != "" {
		spxlog.Error("%s", msg)
	}

	extMgr.OnRuntimePanic(msg)

	if exitOnPanic {
		RequestExit(1)
	}
}

// Panic reports a panic message through the engine.
func Panic(args ...any) {
	msg := fmt.Sprint(args...)
	handlePanic(msg, "", nil, true)
}

// Panicf reports a formatted panic message through the engine.
func Panicf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	handlePanic(msg, "", nil, true)
}

// abortCoroutinesAndReset aborts coroutines and resets the engine.
// Used on web, where the process cannot exit.
func abortCoroutinesAndReset(exitCode int64) {
	co := gco
	// Drain off-thread while the panic caller unwinds.
	go requestResetAfterCoroutinesStop(co, 2*stime.Second, func() {
		extMgr.RequestReset(exitCode)
	})
	if co.IsInCoroutine() {
		co.Abort()
	}
}

func requestResetAfterCoroutinesStop(co *coroutine.Coroutines, timeout stime.Duration, requestReset func()) bool {
	completed := co.RunAfterAbortAll(timeout, func() {
		co.WaitMainThread(requestReset)
	})
	if !completed {
		spxlog.Error("Coroutine shutdown timed out; engine reset was not requested.")
		return false
	}
	spxlog.Debug("Coroutine shutdown completed. Engine reset requested.")
	return true
}

func RequestExit(exitCode int64) {
	if platform.IsWeb() {
		// Web resets instead of exiting.
		abortCoroutinesAndReset(exitCode)
		return
	}
	extMgr.RequestExit(exitCode)
}
