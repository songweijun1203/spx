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
	"github.com/goplus/spbase/mathf"
	coreruntime "github.com/goplus/spx/v3/internal/core/runtime"
	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine"
	itime "github.com/goplus/spx/v3/internal/time"
)

func (p *Game) initEventLoop() {
	// 这里只创建三个长期受管 Thread；它们会分别在通道或 WaitNextFrame 处挂起，
	// 不会在当前初始化调用栈中把循环完整跑完。
	gco.Create("eventLoop", p.eventLoop)
	gco.Create("inputEventLoop", p.inputEventLoop)
	gco.Create("logicLoop", p.logicLoop)
}

func (p *Game) eventLoop(coroutine.Thread) {
	// 通道为空时 WaitForChan 释放脚本执行权；收到事件后按通道顺序串行路由。
	events := p.events
	for {
		p.handleEvent(engine.WaitForChan(events))
	}
}

func (p *Game) inputEventLoop(coroutine.Thread) {
	// 实时输入循环把一帧内的有序键鼠边沿转换成 Game 层事件。
	hooks := inputFrameEventHooks(p.fireEvent)
	// 实时鼠标移动直接更新 swipe；点击和按键仍经 Game.events 交给 eventLoop。
	hooks.OnMouseMove = p.inputMgr.onMouseMove
	coreruntime.RunInputLoop(coreruntime.InputLoopConfig{
		// 输入会话存在时由录制/回放路径独占 ready 队列，本循环暂停消费。
		BeginFrame: func() bool {
			return p.currentInputSession() == nil
		},
		// 位置、按钮边沿/按住位图和键盘边沿均来自当前引擎帧快照。
		CurrentMousePos:        engine.Managers().InputMgr.GetGlobalMousePos,
		InputFrameHooks:        hooks,
		SetMousePos:            p.inputMgr.setMousePos,
		GetMouseInput:          engine.GetMouseInput,
		GetKeyEvents:           engine.GetKeyEvents,
		MouseMovementThreshold: mouseMovementThreshold,
	})
}

func inputFrameEventHooks(emit func(event)) coreruntime.InputFrameHooks {
	return coreruntime.InputFrameHooks{
		// 点击和按键进入 Game.events；eventLoop 再创建相应脚本处理器。
		FireLeftButtonDown: func(point mathf.Vec2) {
			emit(&eventLeftButtonDown{Pos: point})
		},
		FireLeftButtonUp: func(point mathf.Vec2) {
			emit(&eventLeftButtonUp{Pos: point})
		},
		OnMouseMove: func(point mathf.Vec2) {
			emit(&eventMouseMove{Pos: point})
		},
		OnKeyPressed: func(keyID int64) {
			emit(&eventKeyDown{Key: Key(keyID)})
		},
	}
}

func (p *Game) logicLoop(coroutine.Thread) {
	// 逻辑维护循环清理音频/动画完成项并派发到期计时器，不负责主渲染同步。
	frame := coreruntime.LogicFrameConfig[Shape]{
		TempAudios:     []string{},
		TempAnimations: []string{},
		FlushPendingAudio: func(item Shape, tempAudios []string) []string {
			if sprite, ok := item.(*SpriteImpl); ok {
				return sprite.flushPendingAudios(tempAudios)
			}
			return tempAudios
		},
		FlushCompletedAnimations: func(item Shape, tempAnimations []string) []string {
			if sprite, ok := item.(*SpriteImpl); ok {
				return sprite.flushCompletedAnimations(tempAnimations)
			}
			return tempAnimations
		},
		NextTimer: itime.NextTimer,
		FireTimer: func(timestamp int64) {
			p.fireEvent(&eventTimer{Timestamp: timestamp})
		},
	}

	for {
		// 每轮取得稳定的 Shape 快照；处理完成后挂起到下一引擎帧。
		frame.Items = p.shapeMgr.getTempShapes()
		frame.TempAudios, frame.TempAnimations = coreruntime.ProcessLogicFrame(frame)
		engine.WaitNextFrame()
		p.showDebugPanel()
	}
}
