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
	"github.com/goplus/spx/v3/internal/engine"
)

// 下列 queuePen* 方法是 Go 游戏逻辑到 Godot/C++ 画笔系统的统一出口。
// Web/WASM 环境下每次跨边界调用成本较高，因此启用 penSyncBuffer 时先按
// 原始发生顺序缓存 move/down/up/color/size，并在帧尾一次性提交。未启用
// 缓冲的测试或纯 Go 环境则直接调用 PenMgr，二者的可观察顺序保持一致。
func (p *Game) queuePenMove(obj engine.Object, position mathf.Vec2) {
	if p.penSyncBuffer == nil {
		p.engine().PenMgr.MovePenTo(obj, position)
		return
	}
	if p.penSyncBuffer.AddMove(obj, position.X, position.Y) {
		p.flushPenCommands()
	}
}

func (p *Game) queuePenDown(obj engine.Object, moveByMouse bool) {
	if moveByMouse {
		// C++ 的 moveByMouse 模式会在 Godot 更新阶段自行读取鼠标坐标，不能
		// 与队列中的旧 move 命令乱序，所以先通过 barrier 冲刷已有命令。
		// 当前公开的 Sprite.PenDown 传 false，鼠标绘图仍由脚本移动精灵驱动。
		p.penCommandBarrier(func() {
			p.engine().PenMgr.PenDown(obj, true)
		})
		return
	}
	if p.penSyncBuffer == nil {
		p.engine().PenMgr.PenDown(obj, false)
		return
	}
	if p.penSyncBuffer.AddDown(obj, false) {
		p.flushPenCommands()
	}
}

func (p *Game) queuePenUp(obj engine.Object) {
	if p.penSyncBuffer == nil {
		p.engine().PenMgr.PenUp(obj)
		return
	}
	if p.penSyncBuffer.AddUp(obj) {
		p.flushPenCommands()
	}
}

func (p *Game) queuePenColor(obj engine.Object, color mathf.Color) {
	if p.penSyncBuffer == nil {
		p.engine().PenMgr.SetPenColorTo(obj, color)
		return
	}
	if p.penSyncBuffer.AddColor(obj, color.R, color.G, color.B, color.A) {
		p.flushPenCommands()
	}
}

func (p *Game) queuePenSize(obj engine.Object, size float64) {
	if p.penSyncBuffer == nil {
		p.engine().PenMgr.SetPenSizeTo(obj, size)
		return
	}
	if p.penSyncBuffer.AddSetSize(obj, size) {
		p.flushPenCommands()
	}
}

func (p *Game) flushPenCommands() {
	if p.penSyncBuffer == nil {
		return
	}
	// BatchUpdateCommands 是一次同步调用；返回后缓冲区才可复用。
	p.penSyncBuffer.Flush(p.engine().PenMgr.BatchUpdateCommands)
}

func (p *Game) discardPenCommands() {
	if p.penSyncBuffer != nil {
		p.penSyncBuffer.Discard()
	}
}

func (p *Game) penCommandBarrier(operation func()) {
	if p.penSyncBuffer == nil {
		operation()
		return
	}
	// stamp、销毁画笔、调整画布等不能编码进批处理。Barrier 保证它们之前
	// 产生的线段先提交，避免出现“先销毁后移动”或“清屏后旧线又出现”。
	p.penSyncBuffer.Barrier(p.engine().PenMgr.BatchUpdateCommands, operation)
}
