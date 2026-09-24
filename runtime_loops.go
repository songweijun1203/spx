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
	coreruntime.InitLoops(gco.Create, coreruntime.LoopTasks{
		Event: p.eventLoop,
		Input: p.inputEventLoop,
		Logic: p.logicLoop,
	})
}

func (p *Game) eventLoop(coroutine.Thread) {
	coreruntime.RunEventLoop(p.events, p.handleEvent)
}

func (p *Game) inputEventLoop(coroutine.Thread) {
	// RunInputLoop 是游戏输入协程的主体：每轮读取一次输入快照，
	// 把底层键盘/鼠标状态转换成 Game 层事件，然后等待下一帧。
	// 这里传入的回调由 internal/core/runtime.RunInputLoop 调用；
	// 本函数本身不直接处理 Godot 的 InputEvent。
	coreruntime.RunInputLoop(coreruntime.InputLoopConfig{
		// 每轮采样前判断当前是否处于输入录制/回放会话。
		// 返回 false 时，RunInputLoop 不消费实时输入，只等待下一帧，
		// 让 replay 逻辑通过另一条路径提供本帧输入。
		BeginFrame: func() bool {
			return p.currentInputSession() == nil
		},
		// 每轮读取 Godot 当前的全局鼠标坐标，并交给 ProcessInputFrame。
		// Native/Web 的 InputMgr 会在各自平台实现中把坐标转换为 SPX 坐标。
		CurrentMousePos: engine.Managers().InputMgr.GetGlobalMousePos,
		// 读取鼠标左键当前是否处于按下状态。
		// 这是状态查询，不是从 mouseInput.pending/ready 取边沿事件；
		// ProcessInputFrame 会将本帧状态与上一帧状态比较，判断按下或弹起。
		IsLeftButtonPressed: func() bool {
			return engine.IsMouseButtonPressed(MOUSE_BUTTON_LEFT)
		},
		// ProcessInputFrame 检测到 false -> true 时调用这里。
		// 构造高层 eventLeftButtonDown，并通过 p.fireEvent 写入本 Game 的
		// events chan；eventLoop 随后从通道取出并执行点击命中逻辑。
		FireLeftButtonDown: func(point mathf.Vec2) {
			p.fireEvent(&eventLeftButtonDown{Pos: point})
		},
		// ProcessInputFrame 检测到 true -> false 时调用这里。
		// 它与按下事件一样进入 Game.events，最终由 eventLoop 调用
		// Game.doWhenLeftButtonUp()，完成抬起/滑动状态收尾。
		FireLeftButtonUp: func(point mathf.Vec2) {
			p.fireEvent(&eventLeftButtonUp{Pos: point})
		},
		// 更新 Game.inputMgr 中缓存的鼠标位置。
		// RunInputLoop 每轮都会先调用一次；输入位置变化时
		// ProcessInputFrame 还会再次调用，以保持后续点击和感知查询使用最新坐标。
		SetMousePos: p.inputMgr.setMousePos,
		// 实时模式下鼠标移动直接更新 inputMgr，不包装成 Game.events。
		// 输入回放模式会使用另一套配置，把移动封装为 eventMouseMove。
		OnMouseMove: p.inputMgr.onMouseMove,
		// 取出本帧开始时由 onUpdate() 从 keyInput.pending 转入 ready 的
		// 键盘边沿事件。RunInputLoop 会遍历这些事件，但当前高层脚本只对
		// IsPressed=true 的事件生成 eventKeyDown。
		GetKeyEvents: engine.GetKeyEvents,
		// 对每个刚按下的键构造 eventKeyDown，并写入 Game.events。
		// eventLoop 消费该事件后，才会调用脚本事件系统中的 OnKey 处理器。
		OnKeyPressed: func(keyID int64) {
			p.fireEvent(&eventKeyDown{Key: Key(keyID)})
		},
		// 鼠标位置至少变化该距离才视为一次移动，避免微小抖动持续触发移动逻辑。
		MouseMovementThreshold: mouseMovementThreshold,
	})
}

func (p *Game) logicLoop(coroutine.Thread) {
	coreruntime.RunLogicLoop(coreruntime.LogicLoopConfig[Shape]{
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
