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
	"sort"

	"github.com/goplus/spbase/mathf"
	coreruntime "github.com/goplus/spx/v3/internal/core/runtime"
	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine"
)

// inputSessionInput 保存当前录制/回放会话的输入适配状态，把解析后的确定性输入
// 转换为普通 SPX 高层事件，同时维护跨帧的鼠标位置和左键状态。
type inputSessionInput struct {
	// 上一个有效输入 tick 的派生状态，用于识别鼠标移动和按下/抬起边沿。
	lastMousePos          mathf.Vec2
	lastLeftButtonPressed bool
	// 采样和转换时复用的临时切片，避免每帧重复分配。
	mouseEvents []engine.MouseEvent
	keyEvents   []engine.KeyEvent
	// pending 是 OnEngineBeforeUpdate 已解析、等待 OnEngineUpdate 派发的帧快照，
	// 仅由引擎帧线程访问；nil 表示本帧没有可推进的输入 tick。
	pending *inputSessionFrame
}

type inputSessionFrame struct {
	// events 是根据有效输入状态生成的点击、移动和按键高层事件。
	events []event
	// keyEvents 保留回放格式的原始键盘边沿，用于处理录制/回放截图热键。
	keyEvents []InputReplayKeyEvent
}

// prepareInputSessionTick 在条件采样前解析一个输入 tick。
// 它会更新条件查询使用的有效输入状态，但只把高层事件暂存在 session.input.pending；
// 用户处理器和截图请求推迟到逻辑时钟推进后的 dispatchInputSessionTick。
func (p *inputManager) prepareInputSessionTick(session *inputSession, delta float64) bool {
	// beginFrame 保证一个输入会话同一时间只打开一个引擎帧，并拒绝已经结束的会话。
	if !session.beginFrame() {
		return false
	}
	frame, err := p.resolveInputSessionTick(session, delta)
	if err != nil {
		// 解析失败也要关闭本帧，否则会话会永久停在 frameOpen 状态。
		session.endFrame()
		engine.Panic(err)
		return false
	}
	// 建立 BeforeUpdate -> OnEngineUpdate 的交接点。
	session.input.pending = frame
	return true
}

// dispatchInputSessionTick 消费 BeforeUpdate 暂存的输入帧。
// 调用发生在逻辑时钟推进之后：先处理配置的截图热键，再异步派发高层输入事件。
func (p *inputManager) dispatchInputSessionTick(session *inputSession) {
	frame := session.input.pending
	// 先清空，确保同一个 tick 即使发生重入也不会重复派发。
	session.input.pending = nil
	session.captureConfiguredKeyPresses(frame.keyEvents)
	p.dispatchInputSessionEvents(frame.events)
}

// resolveInputSessionTick 把本帧底层输入解析为一个确定性的有效输入帧。
// 录制模式使用现场采样并写入控制器；回放模式由控制器用记录值替换现场状态。
// 返回前只更新查询状态、计算事件列表，不执行任何用户事件处理器。
func (p *inputManager) resolveInputSessionTick(session *inputSession, delta float64) (*inputSessionFrame, error) {
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	c := &session.input
	resolved, err := session.consumeSampledInputTickLocked(delta, func() (InputReplayState, []InputReplayMouseEvent, []InputReplayKeyEvent) {
		pointValue := engine.Managers().InputMgr.GetGlobalMousePos()
		var buttons uint8
		// 输入会话在帧采样时消费 onUpdate 已缓存的鼠标边沿及当前按住状态；
		// 普通实时输入循环不调用 GetMouseInput，而是单独轮询左键状态。
		c.mouseEvents, buttons = engine.GetMouseInput(c.mouseEvents[:0])
		var keysDown []int64
		// 与鼠标同理：输入会话取边沿和当前按住状态快照；普通模式的 inputEventLoop
		// 则通过 GetKeyEvents 消费有序边沿。
		c.keyEvents, keysDown = engine.GetKeyInput(c.keyEvents[:0])

		return InputReplayState{
			Mouse: InputReplayMouse{
				X: float64(pointValue.X),
				Y: float64(pointValue.Y),
			},
			Buttons:  buttons,
			KeysDown: keysDown,
		}, replayMouseEventsFromEngine(c.mouseEvents), replayKeyEventsFromEngine(c.keyEvents)
	})
	if err != nil {
		return nil, err
	}
	effectivePoint := mathf.Vec2{X: resolved.frame.State.Mouse.X, Y: resolved.frame.State.Mouse.Y}
	effectiveLeftPressed := resolved.frame.State.Buttons&(1<<0) != 0
	if resolved.firstTick {
		// 第一帧先对齐派生状态，避免把会话初始按键/位置误判为本帧新事件。
		p.resetInputSessionDerivedState(session, resolved.initial)
	}
	// 后续统一使用控制器解析后的有效边沿：录制时通常等于现场输入，
	// 回放时来自记录文件，而不是当前机器上的真实输入。
	c.mouseEvents = engineMouseEventsFromReplay(resolved.frame.MouseEvents, c.mouseEvents)
	c.keyEvents = engineKeyEventsFromReplay(resolved.frame.KeyEvents, c.keyEvents)
	p.setMousePos(effectivePoint)
	// 本阶段只收集事件对象；真正的 handleEvent 调用留到 OnEngineUpdate。
	inputEvents := make([]event, 0, len(c.mouseEvents)+len(c.keyEvents)+3)

	c.lastMousePos, c.lastLeftButtonPressed = coreruntime.ProcessInputFrame(
		coreruntime.InputFrame{
			Point:                    effectivePoint,
			LastMousePos:             c.lastMousePos,
			LastLeftButtonPressed:    c.lastLeftButtonPressed,
			CurrentLeftButtonPressed: effectiveLeftPressed,
			MouseEvents:              c.mouseEvents,
			KeyEvents:                c.keyEvents,
			MouseMovementThreshold:   mouseMovementThreshold,
		},
		coreruntime.InputFrameHooks{
			FireLeftButtonDown: func(point mathf.Vec2) {
				inputEvents = append(inputEvents, &eventLeftButtonDown{Pos: point})
			},
			FireLeftButtonUp: func(point mathf.Vec2) {
				inputEvents = append(inputEvents, &eventLeftButtonUp{Pos: point})
			},
			SetMousePos: p.setMousePos,
			OnMouseMove: func(point mathf.Vec2) {
				inputEvents = append(inputEvents, &eventMouseMove{Pos: point})
			},
			OnKeyPressed: func(keyID int64) {
				inputEvents = append(inputEvents, &eventKeyDown{Key: Key(keyID)})
			},
		},
	)
	c.clearEvents()
	return &inputSessionFrame{events: inputEvents, keyEvents: resolved.frame.KeyEvents}, nil
}

func (s *inputSession) captureConfiguredKeyPresses(events []InputReplayKeyEvent) {
	if s.captureKey == 0 {
		return
	}
	for _, event := range events {
		if event.Pressed && Key(event.Key) == s.captureKey {
			Snapshot("", nil)
		}
	}
}

func (c *inputSessionInput) clearEvents() {
	c.mouseEvents = c.mouseEvents[:0]
	c.keyEvents = c.keyEvents[:0]
}

func (p *inputManager) dispatchInputSessionEvents(events []event) {
	if len(events) == 0 {
		return
	}
	dispatch := func() {
		for _, event := range events {
			p.g.handleEvent(event)
		}
	}
	if gco == nil {
		dispatch()
		return
	}
	gco.Create(p.g, func(coroutine.Thread) {
		dispatch()
	})
}

func (p *inputManager) resetInputSessionDerivedState(session *inputSession, state InputReplayState) {
	point := mathf.Vec2{X: state.Mouse.X, Y: state.Mouse.Y}
	p.mousePos = point
	session.input.lastMousePos = point
	session.input.lastLeftButtonPressed = state.Buttons&(1<<0) != 0
	p.clickGate.InitWithClock(mouseClickInterval, p.g.inputClock)
	p.swipe.InitWithClock(p.g.inputClock)
}

func (p *inputManager) effectiveKeyPressed(key Key) bool {
	if state, replaying := p.g.currentInputPlaybackState(); replaying {
		if key == KeyAny {
			return len(state.KeysDown) != 0
		}
		keyID := int64(key)
		index := sort.Search(len(state.KeysDown), func(i int) bool {
			return state.KeysDown[i] >= keyID
		})
		return index < len(state.KeysDown) && state.KeysDown[index] == keyID
	}
	return engine.Managers().InputMgr.GetKey(int64(key))
}

func (p *inputManager) effectiveMousePos() mathf.Vec2 {
	if state, replaying := p.g.currentInputPlaybackState(); replaying {
		return mathf.Vec2{X: state.Mouse.X, Y: state.Mouse.Y}
	}
	return p.currentMousePos()
}

func (p *inputManager) effectiveMousePressed() bool {
	if state, replaying := p.g.currentInputPlaybackState(); replaying {
		return state.Buttons&0b011 != 0
	}
	return engine.AnyMouseButtonPressed()
}
