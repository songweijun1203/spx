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
	"github.com/goplus/spx/v3/internal/engine"
)

type InputFrame struct {
	// Point 是本 tick 的有效鼠标位置；录制时来自现场，回放时来自记录。
	Point mathf.Vec2
	// LeftButtonPressed 是采样结束时的左键按住状态，用于校验边沿流并兼容旧回放。
	LeftButtonPressed bool
	// MouseEvents 和 KeyEvents 保留同一 tick 内按原顺序到达的按下/抬起边沿。
	MouseEvents []engine.MouseEvent
	KeyEvents   []engine.KeyEvent
	// MouseMovementThreshold 控制位置变化何时生成高层移动事件。
	MouseMovementThreshold float64
}

// InputFrameState is the consumer-local history used to normalize input
// snapshots and ordered edges into one stream of input events.
type InputFrameState struct {
	// lastMousePos 和 leftButtonPressed 只属于当前消费者；实时循环和输入会话
	// 各自持有状态，切换所有权时通过 Reset 对齐，避免制造跨会话边沿。
	lastMousePos      mathf.Vec2
	leftButtonPressed bool
}

type InputFrameHooks struct {
	// 点击、移动和按键钩子把归一化输入转换成 Game 层事件或即时状态更新。
	FireLeftButtonDown func(mathf.Vec2)
	FireLeftButtonUp   func(mathf.Vec2)
	OnMouseMove        func(mathf.Vec2)
	OnKeyPressed       func(int64)
}

type InputLoopConfig struct {
	InputFrameHooks
	// BeginFrame 返回 false 表示本轮输入由录制/回放会话接管；实时循环只等待下一帧。
	BeginFrame func() bool
	// CurrentMousePos 返回本轮位置；SetMousePos 更新 Game.inputMgr 的即时查询缓存。
	CurrentMousePos func() mathf.Vec2
	SetMousePos     func(mathf.Vec2)
	// GetMouseInput 同时取走有序按钮边沿和采样时的按住位图。
	GetMouseInput func([]engine.MouseEvent) ([]engine.MouseEvent, uint8)
	// GetKeyEvents 取走有序键盘边沿；当前高层 API 只对按下边沿触发 OnKey。
	GetKeyEvents func([]engine.KeyEvent) []engine.KeyEvent
	// 位移任一轴超过该阈值时才调用 OnMouseMove。
	MouseMovementThreshold float64
}

type inputLoopState struct {
	// frameState 保存实时消费者的跨帧派生状态。
	frameState InputFrameState
	// 临时切片跨帧复用，避免输入循环每帧分配。
	mouseEvents []engine.MouseEvent
	keyEvents   []engine.KeyEvent
	// 输入会话接管后置位；恢复实时输入时只同步现状，不补发会话期间的变化。
	wasSuspended bool
}

type LogicFrameConfig[T any] struct {
	Items                    []T
	TempAudios               []string
	TempAnimations           []string
	FlushPendingAudio        func(T, []string) []string
	FlushCompletedAnimations func(T, []string) []string
	NextTimer                func() (int64, bool)
	FireTimer                func(int64)
}

func (s *InputFrameState) Reset(point mathf.Vec2, leftButtonPressed bool) {
	*s = InputFrameState{lastMousePos: point, leftButtonPressed: leftButtonPressed}
}

// Process 将同一 tick 的有序边沿、最终按住状态和鼠标位置归一化为高层事件。
// 边沿优先保持同帧内的真实顺序；最终状态用于旧回放兼容和平台漏报保护。
func (s *InputFrameState) Process(frame InputFrame, hooks InputFrameHooks) {
	leftPressed := s.leftButtonPressed
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
	if frame.LeftButtonPressed != leftPressed {
		if leftPressed {
			hooks.FireLeftButtonUp(frame.Point)
		} else {
			hooks.FireLeftButtonDown(frame.Point)
		}
	}

	dx := frame.Point.X - s.lastMousePos.X
	dy := frame.Point.Y - s.lastMousePos.Y
	if math.Abs(dx) > frame.MouseMovementThreshold || math.Abs(dy) > frame.MouseMovementThreshold {
		hooks.OnMouseMove(frame.Point)
		s.lastMousePos = frame.Point
	}

	for _, ev := range frame.KeyEvents {
		if ev.IsPressed {
			hooks.OnKeyPressed(ev.Id)
		}
	}

	s.leftButtonPressed = frame.LeftButtonPressed
}

func RunInputLoop(cfg InputLoopConfig) {
	state := inputLoopState{}

	for {
		if cfg.BeginFrame != nil && !cfg.BeginFrame() {
			// 暂停期间由输入会话消费 ready 队列；实时消费者恢复时必须重新对齐。
			state.wasSuspended = true
			engine.WaitNextFrame()
			continue
		}
		runInputLoopFrame(cfg, &state)
		engine.WaitNextFrame()
	}
}

func ProcessLogicFrame[T any](cfg LogicFrameConfig[T]) ([]string, []string) {
	// 音频与动画完成队列按舞台 Shape 顺序清理，临时切片由调用方跨帧复用。
	tempAudios := cfg.TempAudios
	for _, item := range cfg.Items {
		tempAudios = cfg.FlushPendingAudio(item, tempAudios)
	}

	tempAnimations := cfg.TempAnimations
	for _, item := range cfg.Items {
		tempAnimations = cfg.FlushCompletedAnimations(item, tempAnimations)
	}

	for {
		// 一帧内取尽已经到期的逻辑计时器，并把每个时间戳转换成 Game 事件。
		targetTimer, ok := cfg.NextTimer()
		if !ok {
			break
		}
		cfg.FireTimer(targetTimer)
	}
	return tempAudios, tempAnimations
}

func runInputLoopFrame(cfg InputLoopConfig, state *inputLoopState) {
	// 位置查询与按键/鼠标 ready 队列属于同一引擎帧快照。
	point := cfg.CurrentMousePos()
	// 每帧都刷新查询缓存；高层感知 API 无需再次跨边界读取引擎坐标。
	cfg.SetMousePos(point)
	var buttons uint8
	// 两个 drain 都复用调用方切片，并在返回时清空各自的 ready 队列。
	state.mouseEvents, buttons = cfg.GetMouseInput(state.mouseEvents[:0])
	state.keyEvents = cfg.GetKeyEvents(state.keyEvents[:0])
	leftButtonPressed := buttons&1 != 0
	if state.wasSuspended {
		// 暂停消费者拥有期间的全部边沿。恢复时仅同步位置和按住状态，
		// 不把会话边界伪造成点击或移动。
		state.frameState.Reset(point, leftButtonPressed)
		state.wasSuspended = false
	} else {
		// InputFrameState 先按顺序派发鼠标边沿，再用最终按住状态校验，
		// 随后处理移动与键盘按下事件。
		state.frameState.Process(
			InputFrame{
				Point:                  point,
				LeftButtonPressed:      leftButtonPressed,
				MouseEvents:            state.mouseEvents,
				KeyEvents:              state.keyEvents,
				MouseMovementThreshold: cfg.MouseMovementThreshold,
			},
			cfg.InputFrameHooks,
		)
	}
}
