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
	coreruntime.InitLoops(gco.Create, p.eventLoop, p.inputEventLoop, p.logicLoop)
}

func (p *Game) eventLoop(me coroutine.Thread) int {
	return coreruntime.RunEventLoop(me, p.events, p.handleEvent)
}

func (p *Game) inputEventLoop(me coroutine.Thread) int {
	return coreruntime.RunInputLoop(me, coreruntime.InputLoopConfig{
		BeginFrame: func() bool {
			return p.currentInputSession() == nil
		},
		CurrentMousePos: func() mathf.Vec2 {
			// Godot 侧先用当前 Camera2D 把窗口坐标换算成世界坐标，再转换成
			// SPX 坐标系。这里每个逻辑帧只读取一次，后续 mouseX/mouseY、
			// 点击命中和滑动识别都使用同一份坐标，避免同一帧内结果不一致。
			curMousePos := p.engine().InputMgr.GetGlobalMousePos()
			return mathf.Vec2{X: float64(curMousePos.X), Y: float64(curMousePos.Y)}
		},
		IsLeftButtonPressed: func() bool {
			// 按钮回调维护的是实时按下状态；输入循环在这里比较前后两帧状态，
			// 生成一次性的按下/松开事件。鼠标画线通常不依赖 OnClick，
			// 而是在脚本的 forever 中通过 mousePressed 持续查询按下状态。
			return engine.IsMouseButtonPressed(MOUSE_BUTTON_LEFT)
		},
		FireLeftButtonDown: func(point mathf.Vec2) {
			p.fireEvent(&eventLeftButtonDown{Pos: point})
		},
		FireLeftButtonUp: func(point mathf.Vec2) {
			p.fireEvent(&eventLeftButtonUp{Pos: point})
		},
		// setMousePos 保存本帧坐标，Game.MouseX/MouseY 直接读取该缓存。
		SetMousePos:  p.inputMgr.setMousePos,
		OnMouseMove:  p.inputMgr.onMouseMove,
		GetKeyEvents: engine.GetKeyEvents,
		OnKeyPressed: func(keyID int64) {
			p.fireEvent(&eventKeyDown{Key: Key(keyID)})
		},
		MouseMovementThreshold: mouseMovementThreshold,
	})
}

func (p *Game) logicLoop(me coroutine.Thread) int {
	return coreruntime.RunLogicLoop(me, coreruntime.LogicLoopConfig[Shape]{
		Items: p.getTempShapes,
		FlushPendingAudio: func(item Shape, tempAudios []string) []string {
			sprite, ok := item.(*SpriteImpl)
			if !ok {
				return tempAudios
			}
			return sprite.flushPendingAudios(tempAudios)
		},
		FlushCompletedAnimations: func(item Shape, tempAnimations []string) []string {
			sprite, ok := item.(*SpriteImpl)
			if !ok {
				return tempAnimations
			}
			return sprite.flushCompletedAnimations(tempAnimations)
		},
		NextTimer: itime.NextTimer,
		FireTimer: func(targetTimer float64) {
			p.fireEvent(&eventTimer{Time: targetTimer})
		},
		ShowDebugPanel: p.showDebugPanel,
	})
}
