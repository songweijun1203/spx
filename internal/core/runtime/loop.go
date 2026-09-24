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

package runtime

import (
	"math"

	"github.com/goplus/spbase/mathf"
	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine"
)

type InputFrame struct {
	Point                    mathf.Vec2
	LastMousePos             mathf.Vec2
	LastLeftButtonPressed    bool
	CurrentLeftButtonPressed bool
	MouseEvents              []engine.MouseEvent
	KeyEvents                []engine.KeyEvent
	MouseMovementThreshold   float64
}

type InputFrameHooks struct {
	FireLeftButtonDown func(mathf.Vec2)
	FireLeftButtonUp   func(mathf.Vec2)
	SetMousePos        func(mathf.Vec2)
	OnMouseMove        func(mathf.Vec2)
	OnKeyPressed       func(int64)
}

type InputLoopConfig struct {
	BeginFrame             func() bool
	EndFrame               func()
	CurrentMousePos        func() mathf.Vec2
	IsLeftButtonPressed    func() bool
	FireLeftButtonDown     func(mathf.Vec2)
	FireLeftButtonUp       func(mathf.Vec2)
	SetMousePos            func(mathf.Vec2)
	OnMouseMove            func(mathf.Vec2)
	GetKeyEvents           func([]engine.KeyEvent) []engine.KeyEvent
	OnKeyPressed           func(int64)
	MouseMovementThreshold float64
}

type inputLoopState struct {
	lastLeftButtonPressed bool
	lastMousePos          mathf.Vec2
	keyEvents             []engine.KeyEvent
	wasSuspended          bool
}

type LogicFrameConfig[T any] struct {
	Items                    []T
	TempAudios               []string
	TempAnimations           []string
	FlushPendingAudio        func(T, []string) []string
	FlushCompletedAnimations func(T, []string) []string
	NextTimer                func() (float64, bool)
	FireTimer                func(float64)
	PollConditions           func()
}

type LogicLoopConfig[T any] struct {
	Items                    func() []T
	FlushPendingAudio        func(T, []string) []string
	FlushCompletedAnimations func(T, []string) []string
	NextTimer                func() (float64, bool)
	FireTimer                func(float64)
	PollConditions           func()
	ShowDebugPanel           func()
}

func RunEventLoop[T any](events chan T, handle func(T)) {
	for {
		handle(engine.WaitForChan(events))
	}
}

func ProcessInputFrame(frame InputFrame, hooks InputFrameHooks) (mathf.Vec2, bool) {
	leftPressed := frame.LastLeftButtonPressed
	for _, event := range frame.MouseEvents {
		if event.Id != 1 || event.IsPressed == leftPressed {
			continue
		}
		if event.IsPressed {
			hooks.FireLeftButtonDown(frame.Point)
		} else {
			hooks.FireLeftButtonUp(frame.Point)
		}
		leftPressed = event.IsPressed
	}
	// Old replay files contain only held-state snapshots. This reconciliation
	// also guards against a platform that reports a state change without an edge.
	if frame.CurrentLeftButtonPressed != leftPressed {
		if leftPressed {
			hooks.FireLeftButtonUp(frame.Point)
		} else {
			hooks.FireLeftButtonDown(frame.Point)
		}
	}

	lastMousePos := frame.LastMousePos
	dx := frame.Point.X - frame.LastMousePos.X
	dy := frame.Point.Y - frame.LastMousePos.Y
	if math.Abs(dx) > frame.MouseMovementThreshold || math.Abs(dy) > frame.MouseMovementThreshold {
		hooks.SetMousePos(frame.Point)
		hooks.OnMouseMove(frame.Point)
		lastMousePos = frame.Point
	}

	for _, ev := range frame.KeyEvents {
		if ev.IsPressed {
			hooks.OnKeyPressed(ev.Id)
		}
	}

	return lastMousePos, frame.CurrentLeftButtonPressed
}

func RunInputLoop(cfg InputLoopConfig) {
	state := inputLoopState{keyEvents: make([]engine.KeyEvent, 0)}

	for {
		if cfg.BeginFrame != nil && !cfg.BeginFrame() {
			state.wasSuspended = true
			engine.WaitNextFrame()
			continue
		}
		runInputLoopFrame(cfg, &state)
		engine.WaitNextFrame()
	}
}

func ProcessLogicFrame[T any](cfg LogicFrameConfig[T]) ([]string, []string) {
	tempAudios := cfg.TempAudios
	for _, item := range cfg.Items {
		tempAudios = cfg.FlushPendingAudio(item, tempAudios)
	}

	tempAnimations := cfg.TempAnimations
	for _, item := range cfg.Items {
		tempAnimations = cfg.FlushCompletedAnimations(item, tempAnimations)
	}

	for {
		targetTimer, ok := cfg.NextTimer()
		if !ok {
			break
		}
		cfg.FireTimer(targetTimer)
	}
	if cfg.PollConditions != nil {
		cfg.PollConditions()
	}
	return tempAudios, tempAnimations
}

func RunLogicLoop[T any](cfg LogicLoopConfig[T]) {
	tempAudios := []string{}
	tempAnimations := []string{}

	for {
		tempAudios, tempAnimations = ProcessLogicFrame(LogicFrameConfig[T]{
			Items:                    cfg.Items(),
			TempAudios:               tempAudios,
			TempAnimations:           tempAnimations,
			FlushPendingAudio:        cfg.FlushPendingAudio,
			FlushCompletedAnimations: cfg.FlushCompletedAnimations,
			NextTimer:                cfg.NextTimer,
			FireTimer:                cfg.FireTimer,
			PollConditions:           cfg.PollConditions,
		})
		engine.WaitNextFrame()
		cfg.ShowDebugPanel()
	}
}

type LoopTasks struct {
	Event func(coroutine.Thread)
	Input func(coroutine.Thread)
	Logic func(coroutine.Thread)
}

// InitLoops registers enabled loops in event, input, then logic order.
func InitLoops(create func(coroutine.ThreadObj, func(coroutine.Thread)) coroutine.Thread, tasks LoopTasks) {
	if tasks.Event != nil {
		create("eventLoop", tasks.Event)
	}
	if tasks.Input != nil {
		create("inputEventLoop", tasks.Input)
	}
	if tasks.Logic != nil {
		create("logicLoop", tasks.Logic)
	}
}

// runInputLoopFrame 处理输入协程的一轮输入采样。
//
// 直接调用方：RunInputLoop()；每轮循环调用一次，然后由 RunInputLoop
// 在外层 WaitNextFrame()。它本身不接收 Godot 的原始 InputEvent，
// 而是通过 cfg 中的回调读取已经同步到 Go 的键盘/鼠标状态，再将
// 状态变化转换为 eventKeyDown、eventLeftButtonDown/Up 等高层事件。
//
// 普通实时模式下，鼠标按钮通过“上一帧状态 vs 当前状态”判断，
// 不直接消费 mouseInput.ready；输入回放模式则由上层把 MouseEvent
// 放入 ProcessInputFrame() 的 InputFrame 中处理。
func runInputLoopFrame(cfg InputLoopConfig, state *inputLoopState) {
	// 某些输入会话需要在本轮结束时提交或释放资源；例如回放帧的收尾。
	if cfg.EndFrame != nil {
		defer cfg.EndFrame()
	}
	// 读取当前鼠标位置。普通模式来自 Godot InputMgr，Web 模式可能来自
	// 当前帧的输入快照；随后把位置写入 Game.inputMgr 的缓存。
	point := cfg.CurrentMousePos()
	cfg.SetMousePos(point)

	// 取出本帧开始时已经从 keyInput.pending 转入 keyInput.ready 的键盘边沿。
	// 这里复用 state.keyEvents 的底层数组，避免每帧重新分配内存。
	state.keyEvents = cfg.GetKeyEvents(state.keyEvents)

	// 读取鼠标左键当前状态。普通模式只轮询左键；右键/中键的详细边沿
	// 由 mouseInput.pending/ready 在输入录制或回放路径中处理。
	currentLeftButtonPressed := cfg.IsLeftButtonPressed()

	if state.wasSuspended {
		// 输入循环上一轮处于回放/输入会话暂停状态，暂停期间的输入由另一条
		// 会话路径负责消费。因此恢复时只同步当前状态，不伪造一次按下、抬起
		// 或鼠标移动事件，避免把会话切换误判为用户操作。
		state.lastMousePos = point
		state.lastLeftButtonPressed = currentLeftButtonPressed
		state.wasSuspended = false
	} else {
		// 把本轮采样结果交给 ProcessInputFrame：
		// - 比较左右帧左键状态，必要时调用 FireLeftButtonDown/Up；
		// - 比较左右帧鼠标位置，超过阈值时调用 OnMouseMove；
		// - 遍历 keyEvents，对按下事件调用 OnKeyPressed。
		// 这些回调由 Game.inputEventLoop 配置：按键/点击通常写入
		// Game.events，实时鼠标移动则直接更新 inputMgr。
		state.lastMousePos, state.lastLeftButtonPressed = ProcessInputFrame(
			InputFrame{
				Point:                    point,
				LastMousePos:             state.lastMousePos,
				LastLeftButtonPressed:    state.lastLeftButtonPressed,
				CurrentLeftButtonPressed: currentLeftButtonPressed,
				KeyEvents:                state.keyEvents,
				MouseMovementThreshold:   cfg.MouseMovementThreshold,
			},
			InputFrameHooks{
				FireLeftButtonDown: cfg.FireLeftButtonDown,
				FireLeftButtonUp:   cfg.FireLeftButtonUp,
				SetMousePos:        cfg.SetMousePos,
				OnMouseMove:        cfg.OnMouseMove,
				OnKeyPressed:       cfg.OnKeyPressed,
			},
		)
	}
	// 本轮事件已经被 ProcessInputFrame 消费；保留底层数组容量供下一轮复用。
	state.keyEvents = state.keyEvents[:0]
}
