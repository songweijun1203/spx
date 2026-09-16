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

// -----------------------------------------------------------------------------
// Color
// -----------------------------------------------------------------------------
type PenColorParam int

const (
	PenHue PenColorParam = iota
	PenSaturation
	PenBrightness
	PenTransparency
	PenNone
)

// -----------------------------------------------------------------------------
// Pen
// -----------------------------------------------------------------------------
// PenUp 结束当前笔画。抬笔后精灵仍可移动，但移动不会产生线段；下一次
// PenDown 会从当时的精灵位置开始一条新笔画。
func (p *SpriteImpl) PenUp() {
	p.pen().penUp()
}

// PenDown 把当前精灵当作“笔尖”落到共享画布上。它只切换画笔状态并在
// 当前点画一个圆点，真正的连续线段由之后的 SetXYpos/step/glide 等移动
// 操作触发。因此用鼠标绘图时应先把精灵移到 mouseX/mouseY，再调用 PenDown。
func (p *SpriteImpl) PenDown() {
	p.pen().penDown()
}

func (p *SpriteImpl) Stamp() {
	p.pen().stamp()
}

// -----------------------------------------------------------------------------
// Color Control
// -----------------------------------------------------------------------------
func (p *SpriteImpl) SetPenColor__0(color Color) {
	p.pen().setPenColor(color)
}

func (p *SpriteImpl) SetPenColor__1(kind PenColorParam, value float64) {
	p.pen().setPenColorParam(kind, value)
}

func (p *SpriteImpl) ChangePenColor(kind PenColorParam, delta float64) {
	p.pen().changePenColor(kind, delta)
}

func (p *SpriteImpl) SetPenHue(value float64) {
	p.pen().setPenHue(value)
}

func (p *SpriteImpl) ChangePenHue(delta float64) {
	p.pen().changePenHue(delta)
}

func (p *SpriteImpl) SetPenShade(value float64) {
	p.pen().setPenShade(value)
}

func (p *SpriteImpl) ChangePenShade(delta float64) {
	p.pen().changePenShade(delta)
}

// -----------------------------------------------------------------------------
// Size
// -----------------------------------------------------------------------------
func (p *SpriteImpl) SetPenSize(size float64) {
	p.pen().setPenSize(size)
}

func (p *SpriteImpl) ChangePenSize(delta float64) {
	p.pen().changePenSize(delta)
}

// -----------------------------------------------------------------------------
// Internals
// -----------------------------------------------------------------------------
func (p *SpriteImpl) movePen(x, y float64) {
	// 所有会改变精灵逻辑坐标的入口最终都会经过这里。penComponent 仅在
	// 落笔状态下转发目标坐标，所以普通移动不需要为画笔做额外判断。
	p.pen().movePen(x, y)
}
