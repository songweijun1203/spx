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
	"math"

	"github.com/goplus/spbase/mathf"
	coreproject "github.com/goplus/spx/v3/internal/core/project"
	scheduler "github.com/goplus/spx/v3/internal/engine"
	engine "github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

// ============================================================================
// Pen Component
// ============================================================================
// This component encapsulates all pen drawing functionality.

// scratchLegacyPenState models Scratch 2's pen hue/shade pair, which is
// distinct from the HSV pen color params exposed elsewhere in the engine.
type scratchLegacyPenState struct {
	hue   float64
	shade float64
}

const (
	scratchLegacyDefaultPenHue   = 66.66
	scratchLegacyDefaultPenShade = 50
)

type penComponent struct {
	componentBase

	// Pen properties.
	penColor        mathf.Color
	penWidth        float64
	penHue          float64
	legacyPenColor  scratchLegacyPenState
	penSaturation   float64
	penBrightness   float64
	penTransparency float64

	// Runtime state.
	// isPenDown 是 Go 侧的逻辑落笔状态，用于决定精灵移动是否要生成画笔命令。
	// penObj 是 C++ 侧 SpxPen 的句柄，采用懒创建：从未用过画笔的精灵不会
	// 占用后端对象。克隆会继承落笔/样式状态，但拥有自己的后端画笔对象。
	isPenDown bool
	penObj    *engine.Object
}

// ============================================================================
// Lifecycle
// ============================================================================

// initialize initializes the pen component from config.
func (p *penComponent) initialize(sprite *SpriteImpl, spriteCfg *coreproject.SpriteConfig) {
	p.componentBase.initialize(sprite, spriteCfg)
	p.penColor = mathf.NewColorRGBAi(66, 133, 244, 255)
	p.penWidth = 1
	p.syncPenColorComponents()
	p.legacyPenColor = newScratchLegacyPenState()
	p.isPenDown = false
	p.penObj = nil
}

// cloneFrom creates a new pen component by cloning from source.
func (p *penComponent) cloneFrom(src component, newSprite *SpriteImpl) component {
	srcPen := src.(*penComponent)
	return &penComponent{
		componentBase:   componentBase{sprite: newSprite},
		penColor:        srcPen.penColor,
		penWidth:        srcPen.penWidth,
		penHue:          srcPen.penHue,
		legacyPenColor:  srcPen.legacyPenColor,
		penSaturation:   srcPen.penSaturation,
		penBrightness:   srcPen.penBrightness,
		penTransparency: srcPen.penTransparency,
		isPenDown:       srcPen.isPenDown,
		penObj:          nil,
	}
}

// onDestroy cleans up when the component is destroyed.
func (p *penComponent) onDestroy() {
	p.destroyPen()
}

// ============================================================================
// Pen Control
// ============================================================================

func (p *penComponent) penUp() {
	// 重复抬笔直接忽略，同时也不会仅为了 PenUp 创建一个无用的后端画笔。
	if !p.isPenDown {
		return
	}
	p.isPenDown = false
	if p.penObj == nil {
		return
	}
	p.sprite.g.queuePenUp(*p.penObj)
}

func (p *penComponent) penDown() {
	// 落笔本身会在当前坐标画出一个圆点，所以即使精灵不移动也需要重绘。
	scheduler.RequestRedraw()
	wasDown := p.isPenDown
	created := p.checkOrCreatePen()
	// 新建画笔，或从抬笔切换到落笔时，先把 Go 侧保存的粗细和颜色同步过去。
	// 已经落笔时重复 PenDown 仍会发给后端，以保持“再画一次当前点”的语义，
	// 但不必重复发送没有变化的样式。
	if !wasDown || created {
		p.syncPenAppearance()
	}
	// 命令顺序是 size/color -> move -> down。先定位到精灵当前逻辑坐标，
	// 再落笔，可防止新笔画从后端画笔的默认位置连接过来。
	x, y := p.sprite.getXY()
	p.syncPenPosition(x, y)
	p.isPenDown = true
	p.sprite.g.queuePenDown(*p.penObj, false)
}

func (p *penComponent) stamp() {
	scheduler.RequestRedraw()
	p.checkOrCreatePen()
	x, y := p.sprite.getXY()
	applyRenderOffset(p.sprite, &x, &y)
	rotationRadians, scale := p.getPenStampTransform()
	obj := *p.penObj
	texturePath := p.sprite.getCostumeAssetPath()
	p.sprite.g.penCommandBarrier(func() {
		p.engine().PenMgr.PenStampWithTransform(
			obj,
			texturePath,
			mathf.NewVec2(x, y),
			rotationRadians,
			scale,
		)
	})
}

// ============================================================================
// Pen Size Control
// ============================================================================

func (p *penComponent) setPenSize(size float64) {
	if p.penObj != nil && nearlyEqualPenValue(p.penWidth, size) {
		return
	}
	p.ensureClonePenReady()
	p.penWidth = size
	p.sprite.g.queuePenSize(*p.penObj, size)
}

func (p *penComponent) changePenSize(delta float64) {
	p.setPenSize(p.penWidth + delta)
}

// ============================================================================
// Pen Color Control
// ============================================================================

func (p *penComponent) setPenColor(color Color) {
	nextColor := toMathfColor(color)
	if p.penObj != nil && samePenColor(p.penColor, nextColor) {
		return
	}
	p.penColor = nextColor
	p.ensureClonePenReady()
	p.applyPenColorProperty()
	p.syncLegacyPenStateFromColor()
}

// Legacy Color

func (p *penComponent) setPenHue(value float64) {
	nextValue := wrapScratchPenColorPercent(value / 2)
	if p.penObj != nil &&
		nearlyEqualPenValue(p.legacyPenColor.hue, nextValue) &&
		nearlyEqualPenValue(p.penTransparency, 0) {
		return
	}
	p.updateLegacyPenState(p.legacyPenColor.withHue(nextValue), true)
}

func (p *penComponent) changePenHue(delta float64) {
	nextValue := wrapScratchPenColorPercent(p.legacyPenColor.hue + delta/2)
	if p.penObj != nil && nearlyEqualPenValue(p.legacyPenColor.hue, nextValue) {
		return
	}
	p.updateLegacyPenState(p.legacyPenColor.withHue(nextValue), false)
}

func (p *penComponent) setPenShade(value float64) {
	nextValue := wrapScratchLegacyPenShade(value)
	if p.penObj != nil && nearlyEqualPenValue(p.legacyPenColor.shade, nextValue) {
		return
	}
	p.updateLegacyPenState(p.legacyPenColor.withShade(nextValue), false)
}

func (p *penComponent) changePenShade(delta float64) {
	p.setPenShade(p.legacyPenColor.shade + delta)
}

// HSV Color

func (p *penComponent) setPenColorParam(kind PenColorParam, value float64) {
	switch kind {
	case PenHue:
		p.setPenHueParam(value)
	case PenSaturation:
		p.setPenSaturation(value)
	case PenBrightness:
		p.setPenBrightness(value)
	case PenTransparency:
		p.setPenTransparency(value)
	case PenNone:
		return
	}
}

func (p *penComponent) changePenColor(kind PenColorParam, delta float64) {
	switch kind {
	case PenHue:
		p.changePenHueParam(delta)
	case PenSaturation:
		p.changePenSaturation(delta)
	case PenBrightness:
		p.changePenBrightness(delta)
	case PenTransparency:
		p.changePenTransparency(delta)
	case PenNone:
		return
	}
}

func (p *penComponent) setPenHueParam(value float64) {
	nextValue := wrapScratchPenColorPercent(value)
	if p.penObj != nil && nearlyEqualPenValue(p.penHue, nextValue) {
		return
	}
	p.ensureClonePenReady()
	p.penHue = nextValue
	p.legacyPenColor.hue = p.penHue
	p.applyPenHsvProperty()
}

func (p *penComponent) changePenHueParam(delta float64) {
	p.setPenHueParam(p.penHue + delta)
}

func (p *penComponent) setPenSaturation(value float64) {
	p.setPenHsvComponent(&p.penSaturation, value)
}

func (p *penComponent) changePenSaturation(delta float64) {
	p.setPenSaturation(p.penSaturation + delta)
}

func (p *penComponent) setPenBrightness(value float64) {
	p.setPenHsvComponent(&p.penBrightness, value)
}

func (p *penComponent) changePenBrightness(delta float64) {
	p.setPenBrightness(p.penBrightness + delta)
}

func (p *penComponent) setPenTransparency(value float64) {
	p.setPenHsvComponent(&p.penTransparency, value)
}

func (p *penComponent) changePenTransparency(delta float64) {
	p.setPenTransparency(p.penTransparency + delta)
}

// ============================================================================
// Internal Pen Management
// ============================================================================

func (p *penComponent) checkOrCreatePen() bool {
	if p.penObj == nil {
		// CreatePen 必须立即返回对象句柄，无法放进只负责“无返回值命令”的
		// PenSyncBuffer；后续移动、落笔和样式更新都通过该句柄找到同一支笔。
		obj := p.engine().PenMgr.CreatePen()
		p.penObj = &obj
		p.penTransparency = alphaToTransparency(p.penColor.A)
		return true
	}
	return false
}

func (p *penComponent) destroyPen() {
	if p.penObj != nil {
		obj := *p.penObj
		p.sprite.g.penCommandBarrier(func() {
			p.engine().PenMgr.DestroyPen(obj)
		})
		p.penObj = nil
	}
}

func (p *penComponent) movePen(x, y float64) {
	// transformComponent 会把每次位置变化都送到这里，但只有落笔期间才需要
	// 记录轨迹。x/y 是已经过舞台边界修正的目标逻辑坐标。
	if !p.isPenDown {
		return
	}
	scheduler.RequestRedraw()
	// 克隆可能继承 isPenDown，却还没有自己的 penObj；第一次移动时在此补齐
	// 后端对象、样式、起点和落笔状态，然后再追加本次目标点。
	p.ensureClonePenReady()
	p.syncPenPosition(x, y)
}

func (p *penComponent) syncPenPosition(x, y float64) {
	if p.penObj == nil {
		return
	}
	// 线条跟随的是精灵的逻辑位置，不叠加服装的旋转中心/渲染偏移；这样
	// mouseX/mouseY、精灵位置和画笔位置始终处于同一个 SPX 世界坐标系。
	p.sprite.g.queuePenMove(*p.penObj, mathf.NewVec2(x, y))
}

func (p *penComponent) getPenStampTransform() (rotationRadians float64, scale mathf.Vec2) {
	rotation, scaleX, scaleY := getRenderRotationAndScale(p.sprite)
	renderScale := p.sprite.getCostumeRenderScale()
	return engine.DegToRad(rotation), mathf.NewVec2(scaleX*renderScale, scaleY*renderScale)
}

// ============================================================================
// Pen Appearance
// ============================================================================

func (p *penComponent) applyPenColorProperty() {
	p.checkOrCreatePen()
	p.syncPenColorComponents()
	p.updatePenColor()
}

func (p *penComponent) syncPenColorComponents() {
	h, s, v := p.penColor.ToHSV()
	p.penHue = hueToPercent(h)
	p.penSaturation = normalizedToPercent(s)
	p.penBrightness = normalizedToPercent(v)
	p.penTransparency = alphaToTransparency(p.penColor.A)
}

func (p *penComponent) applyPenHsvProperty() {
	p.penColor = mathf.NewColorHSV(percentToHue(p.penHue), percentToNormalized(p.penSaturation), percentToNormalized(p.penBrightness))
	p.penColor.A = transparencyToAlpha(p.penTransparency)
	p.updatePenColor()
}

func (p *penComponent) applyLegacyPenColor() {
	p.penColor = p.legacyPenColor.color(p.penTransparency)
	p.syncPenColorComponents()
	p.legacyPenColor.hue = p.penHue
	p.updatePenColor()
}

func (p *penComponent) setPenHsvComponent(component *float64, value float64) {
	nextValue := mathf.Clamp(value, 0, 100)
	if p.penObj != nil && nearlyEqualPenValue(*component, nextValue) {
		return
	}
	p.ensureClonePenReady()
	*component = nextValue
	p.applyPenHsvProperty()
}

func (p *penComponent) syncPenAppearance() {
	p.sprite.g.queuePenSize(*p.penObj, p.penWidth)
	p.sprite.g.queuePenColor(*p.penObj, p.penColor)
}

func (p *penComponent) ensureClonePenReady() {
	created := p.checkOrCreatePen()
	if !created || !p.isPenDown {
		return
	}
	// 克隆第一次移动时，先在旧坐标建立起点，再进入落笔状态；调用者随后
	// 会发送新坐标，于是后端能画出从克隆当前位置到目标位置的第一段线。
	p.syncPenAppearance()
	x, y := p.sprite.getXY()
	p.syncPenPosition(x, y)
	p.sprite.g.queuePenDown(*p.penObj, false)
}

func (p *penComponent) syncLegacyPenStateFromColor() {
	p.legacyPenColor.syncFromHSV(p.penHue, p.penBrightness)
}

func (p *penComponent) updateLegacyPenState(state scratchLegacyPenState, resetTransparency bool) {
	p.ensureClonePenReady()
	p.legacyPenColor = state
	if resetTransparency {
		p.penTransparency = 0
	}
	p.applyLegacyPenColor()
}

func (p *penComponent) updatePenColor() {
	p.sprite.g.queuePenColor(*p.penObj, p.penColor)
}

func (s scratchLegacyPenState) withHue(hue float64) scratchLegacyPenState {
	s.hue = hue
	return s
}

func (s scratchLegacyPenState) withShade(shade float64) scratchLegacyPenState {
	s.shade = shade
	return s
}

func (s *scratchLegacyPenState) syncFromHSV(hue, brightness float64) {
	s.hue = hue
	s.shade = brightness / 2
}

func (s scratchLegacyPenState) color(transparency float64) mathf.Color {
	return makeScratchLegacyPenColor(s.hue, s.shade, transparency)
}

// ============================================================================
// Pen Helpers
// ============================================================================

func hueToPercent(hue float64) float64 {
	return (hue / 360) * 100
}

func percentToHue(percent float64) float64 {
	return (percent / 100) * 360
}

func normalizedToPercent(normalized float64) float64 {
	return normalized * 100
}

func percentToNormalized(percent float64) float64 {
	return percent / 100
}

func alphaToTransparency(alpha float64) float64 {
	return normalizedToPercent(1 - alpha)
}

func transparencyToAlpha(transparency float64) float64 {
	return 1 - percentToNormalized(transparency)
}

func newScratchLegacyPenState() scratchLegacyPenState {
	return scratchLegacyPenState{
		hue:   scratchLegacyDefaultPenHue,
		shade: scratchLegacyDefaultPenShade,
	}
}

func makeScratchLegacyPenColor(hue, shade, transparency float64) mathf.Color {
	baseColor := mathf.NewColorHSV(percentToHue(hue), 1, 1)
	normalizedShade := shade
	if normalizedShade > 100 {
		normalizedShade = 200 - normalizedShade
	}
	if normalizedShade < 50 {
		baseColor = mathf.ColorBlack().Lerp(baseColor, (10+normalizedShade)/60)
	} else {
		baseColor = baseColor.Lerp(mathf.ColorWhite(), (normalizedShade-50)/60)
	}
	baseColor.A = transparencyToAlpha(transparency)
	return baseColor
}

func wrapScratchLegacyPenShade(shade float64) float64 {
	shade = math.Mod(shade, 200)
	if shade < 0 {
		shade += 200
	}
	return shade
}

func wrapScratchPenColorPercent(value float64) float64 {
	value = math.Mod(value, 100)
	if value < 0 {
		value += 100
	}
	return value
}

func nearlyEqualPenValue(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func samePenColor(a, b mathf.Color) bool {
	return nearlyEqualPenValue(a.R, b.R) &&
		nearlyEqualPenValue(a.G, b.G) &&
		nearlyEqualPenValue(a.B, b.B) &&
		nearlyEqualPenValue(a.A, b.A)
}
