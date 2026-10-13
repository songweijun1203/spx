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

// inputSessionInput 保存录制/回放消费者的派生状态和复用缓冲区。
// 它与实时 inputEventLoop 各自持有 InputFrameState，避免跨消费者污染边沿历史。
type inputSessionInput struct {
	// 上一个有效输入 tick 的位置和左键状态。
	frameState coreruntime.InputFrameState
	// 采样和格式转换时复用的临时切片。
	mouseEvents []engine.MouseEvent
	keyEvents   []engine.KeyEvent
	// pending 由 BeforeUpdate 写入、OnEngineUpdate 消费，仅在引擎帧线程访问。
	pending *inputSessionFrame
}

type inputSessionFrame struct {
	// events 是从有效输入状态生成的点击、移动和按键高层事件。
	events []event
	// keyEvents 保留回放格式边沿，用于处理配置的截图热键。
	keyEvents []InputReplayKeyEvent
}

// prepareInputSessionTick 在条件采样前解析一个 tick，但把用户处理器和截图请求
// 推迟到逻辑时钟推进后的 dispatchInputSessionTick。
func (p *inputManager) prepareInputSessionTick(session *inputSession, delta float64) bool {
	if !session.beginFrame() {
		return false
	}
	frame, err := p.resolveInputSessionTick(session, delta)
	if err != nil {
		// 失败也要关闭 frameOpen，避免会话永久停在半帧状态。
		session.endFrame()
		engine.Panic(err)
		return false
	}
	// 建立 BeforeUpdate 到 OnEngineUpdate 的显式交接点。
	session.input.pending = frame
	return true
}

func (p *inputManager) dispatchInputSessionTick(session *inputSession) {
	frame := session.input.pending
	// 先清空，防止事件处理发生重入时重复派发同一个 tick。
	session.input.pending = nil
	session.captureConfiguredKeyPresses(frame.keyEvents)
	p.dispatchInputSessionEvents(frame.events)
}

func (p *inputManager) resolveInputSessionTick(session *inputSession, delta float64) (*inputSessionFrame, error) {
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	c := &session.input
	resolved, err := session.consumeSampledInputTickLocked(delta, func() (InputReplayState, []InputReplayMouseEvent, []InputReplayKeyEvent) {
		pointValue := engine.Managers().InputMgr.GetGlobalMousePos()
		var buttons uint8
		// 输入会话和实时循环使用同一鼠标快照 API；BeginFrame 保证同一帧只有
		// 当前消费者取走边沿及按住状态。
		c.mouseEvents, buttons = engine.GetMouseInput(c.mouseEvents[:0])
		var keysDown []int64
		// 会话还需要当前按住键集合写入回放状态，因此使用 GetKeyInput；
		// 实时循环只需有序边沿，使用 GetKeyEvents。
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
	if resolved.firstTick {
		// 第一帧先对齐派生状态，避免把会话初始位置/按钮误判为新事件。
		p.resetInputSessionDerivedState(session, resolved.initial)
	}
	// 后续统一使用控制器解析的有效边沿：录制时来自现场，回放时来自记录文件。
	c.mouseEvents = engineMouseEventsFromReplay(resolved.frame.MouseEvents, c.mouseEvents)
	c.keyEvents = engineKeyEventsFromReplay(resolved.frame.KeyEvents, c.keyEvents)
	p.setMousePos(effectivePoint)
	// 本阶段只收集事件对象；真正的 handleEvent 留到 OnEngineUpdate。
	inputEvents := make([]event, 0, len(c.mouseEvents)+len(c.keyEvents)+3)

	c.frameState.Process(
		coreruntime.InputFrame{
			Point:                  effectivePoint,
			LeftButtonPressed:      resolved.frame.State.Buttons&1 != 0,
			MouseEvents:            c.mouseEvents,
			KeyEvents:              c.keyEvents,
			MouseMovementThreshold: mouseMovementThreshold,
		},
		inputFrameEventHooks(func(ev event) {
			inputEvents = append(inputEvents, ev)
		}),
	)
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
		// 测试或引擎尚未安装协程管理器时保持同步语义。
		dispatch()
		return
	}
	gco.Create(p.g, func(coroutine.Thread) {
		dispatch()
	})
}

func (p *inputManager) resetInputSessionDerivedState(session *inputSession, state InputReplayState) {
	// 会话初态同时重置输入帧历史、连击门和滑动识别器。
	point := mathf.Vec2{X: state.Mouse.X, Y: state.Mouse.Y}
	p.mousePos = point
	session.input.frameState.Reset(point, state.Buttons&1 != 0)
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
