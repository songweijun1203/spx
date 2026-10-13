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
	"github.com/goplus/spx/v3/internal/base/sliceutil"
	"github.com/goplus/spx/v3/internal/engine"
	spxlog "github.com/goplus/spx/v3/internal/log"
	itime "github.com/goplus/spx/v3/internal/time"
	"github.com/goplus/spx/v3/internal/ui"
)

// Scratch renders the shared pen canvas above the backdrop and below every
// managed sprite. Keep the pen layer explicit so sprite ordering cannot reuse it.
const (
	penLayer         = 0
	firstSpriteLayer = penLayer + 1
)

// Scratch shares one limit across all sprites, including hidden clones.
const maxClones = 300

// shapeManager 管理当前舞台 Shape 的顺序、克隆额度、延迟销毁和帧级复用缓冲。
// items 的顺序同时决定默认渲染层和事件目标顺序；named 是按需构建的精灵名称索引。
type shapeManager struct {
	// cloneCount 已进入 items，pendingClones 已预占额度但仍在初始化。
	cloneCount    int
	pendingClones int
	// items 是权威活动列表；tempItems 为遍历提供不受中途增删影响的稳定快照。
	items     []Shape
	named     map[string][]*SpriteImpl
	tempItems []Shape
	// destroyItems 延迟到代理同步批次统一向 Godot 发送删除 ID。
	destroyItems []Shape
	// 以下缓冲用于稳定、可复现地计算同帧多个说话气泡的布局。
	textBubbles            []*textBubble
	activeTextBubbles      []*textBubble
	sayLayouts             []ui.SayBubbleLayout
	nextTextBubbleLayoutID uint64
}

func resetSlice[T any](items []T, initialCap int) []T {
	// clear 释放旧元素引用，同时保留底层容量供下一局/下一帧复用。
	if items == nil && initialCap > 0 {
		return make([]T, 0, initialCap)
	}
	clear(items)
	return items[:0]
}

// init 清空场景索引和计数，同时尽量保留高频切片分配。
func (s *shapeManager) init() {
	s.cloneCount = 0
	s.pendingClones = 0
	s.named = nil
	s.items = resetSlice(s.items, 64)
	s.tempItems = resetSlice(s.tempItems, 50)
	s.destroyItems = resetSlice(s.destroyItems, 16)
	s.textBubbles = resetSlice(s.textBubbles, 0)
	s.activeTextBubbles = resetSlice(s.activeTextBubbles, 0)
	s.sayLayouts = resetSlice(s.sayLayouts, 0)
	s.nextTextBubbleLayoutID = 0
}

// reset 先清除 Godot 侧全部精灵，再复位 Go 侧场景和缓冲。
func (s *shapeManager) reset() {
	engine.ClearAllSprites()
	s.init()
}

// flushActivate 推进 Monitor 等非精灵 Shape；精灵和气泡由各自同步阶段处理。
func (s *shapeManager) flushActivate(items []Shape) {
	if len(items) == 0 {
		return
	}

	delta := itime.DeltaTime()
	for _, item := range items {
		switch v := item.(type) {
		case *SpriteImpl, *textBubble, *quoterBubble:
			continue
		case *Monitor:
			v.onUpdate(delta)
		default:
			if updater, ok := item.(interface{ onUpdate(float64) }); ok {
				updater.onUpdate(delta)
			}
		}
	}
}

func (s *shapeManager) layoutTextBubbles(items []Shape) {
	// 先按稳定 layoutID 收集可见气泡；拓扑或输入不变时跳过求解。
	s.textBubbles = resetSlice(s.textBubbles, 0)
	s.sayLayouts = resetSlice(s.sayLayouts, 0)

	for _, item := range items {
		bubble, ok := item.(*textBubble)
		if !ok || bubble.panel == nil || !bubble.sprite.Visible() {
			continue
		}
		s.textBubbles = append(s.textBubbles, bubble)
	}

	sortTextBubblesByLayoutID(s.textBubbles)
	topologyChanged := len(s.textBubbles) != len(s.activeTextBubbles)
	if !topologyChanged {
		for i, bubble := range s.textBubbles {
			if bubble != s.activeTextBubbles[i] {
				topologyChanged = true
				break
			}
		}
	}
	if topologyChanged {
		s.activeTextBubbles = append(resetSlice(s.activeTextBubbles, 0), s.textBubbles...)
	}
	if len(s.textBubbles) == 0 {
		return
	}

	winSize := s.textBubbles[0].sprite.g.getWindowSize()
	context := ui.NewSayBubbleLayoutContext(winSize)
	needsResolve := topologyChanged
	for _, bubble := range s.textBubbles {
		center, size := bubble.getBounds()
		layout := context.NewLayout(bubble.layoutID, center, size, bubble.content)
		if bubble.hasLayout {
			layout = layout.WithPreviousDirection(bubble.layout)
			if !bubble.layout.SameInput(layout) {
				needsResolve = true
			}
		} else {
			needsResolve = true
		}
		s.sayLayouts = append(s.sayLayouts, layout)
	}
	if !needsResolve {
		return
	}

	ui.ResolveSayBubbleLayouts(s.sayLayouts)
	for i, bubble := range s.textBubbles {
		bubble.setLayout(s.sayLayouts[i])
	}
}

func (s *shapeManager) collectProxyUpdates(items []Shape, buffer *engine.SpriteSyncBuffer) {
	// 只收集 SpriteImpl；Monitor、Measure 和气泡没有 SpriteSyncBuffer 代理记录。
	for _, item := range items {
		if sprite, ok := item.(*SpriteImpl); ok {
			sprite.collectProxyUpdate(buffer)
		}
	}
}

// flushDestroy 把延迟销毁的精灵 ID 写入同步缓冲并断开 Go 代理引用。
func (s *shapeManager) flushDestroy(buffer *engine.SpriteSyncBuffer) {
	if len(s.destroyItems) == 0 {
		return
	}

	for _, item := range s.destroyItems {
		if sprite, ok := item.(*SpriteImpl); ok && sprite.runtimeState.SyncSprite != nil {
			buffer.AddDelete(int64(sprite.runtimeState.SyncSprite.Id))
			sprite.runtimeState.SyncSprite = nil
		}
	}

	s.destroyItems = resetSlice(s.destroyItems, 0)
}

// add 把 Shape 立即加入活动列表，并使按名称缓存失效。
func (s *shapeManager) add(shape Shape) {
	if sprite, ok := shape.(*SpriteImpl); ok && sprite.IsCloned() {
		s.cloneCount++
	}
	if bubble, ok := shape.(*textBubble); ok && bubble.layoutID == 0 {
		s.nextTextBubbleLayoutID++
		if s.nextTextBubbleLayoutID == 0 {
			s.nextTextBubbleLayoutID++
		}
		bubble.layoutID = s.nextTextBubbleLayoutID
	}
	s.items = append(s.items, shape)
	clear(s.named)
}

// remove 只登记延迟销毁；真正的 Godot 节点删除在 flushDestroy 批次完成。
func (s *shapeManager) remove(shape Shape) {
	s.destroyItems = append(s.destroyItems, shape)
}

// addClonedShape inserts a clone immediately behind the target it copied. This
// matches Scratch: repeated clones of one target are ordered oldest to newest,
// with the original target still in front of all of them.
func (s *shapeManager) addClonedShape(src, clone Shape) {
	idx := s.findShapeIndex(src)
	if idx < 0 {
		spxlog.Debug("AddClonedShape: cloning a deleted sprite")
		gco.StopCurrent()
		return
	}

	s.items = sliceutil.InsertAt(s.items, idx, clone)
	clear(s.named)
	if sprite, ok := clone.(*SpriteImpl); ok && sprite.IsCloned() {
		s.cloneCount++
	}
	s.updateRenderLayers()
}

// reserveClone 为尚未进入活动列表的克隆预占名额。
//
// 直接调用方：createRuntimeClone；总体流程调用方：精灵脚本的 Clone API。克隆初始化
// 可能挂起或继续创建克隆，pendingClones 可防止这些对象绕过全局 maxClones 限制。
func (s *shapeManager) reserveClone() bool {
	if s.cloneCount+s.pendingClones >= maxClones {
		return false
	}

	s.pendingClones++
	return true
}

// removeShape 从活动顺序删除对象、更新克隆计数，并安排代理延迟销毁。
func (s *shapeManager) removeShape(child Shape) {
	idx := s.findShapeIndex(child)
	if idx < 0 {
		return
	}

	s.items = sliceutil.DeleteAt(s.items, idx)
	clear(s.named)
	if sprite, ok := child.(*SpriteImpl); ok && sprite.IsCloned() {
		s.cloneCount--
	}
	s.remove(child)
	s.updateRenderLayers()
}

// activateShape 把对象移到最前显示端，并重算默认渲染层。
func (s *shapeManager) activateShape(child Shape) {
	idx := s.findShapeIndex(child)
	if idx < 0 || idx == len(s.items)-1 {
		return
	}

	s.items = sliceutil.MoveToEnd(s.items, idx)
	clear(s.named)
	s.updateRenderLayers()
}

// goBackLayers moves a sprite forward or backward by n layers.
func (s *shapeManager) goBackLayers(spr *SpriteImpl, n int) {
	if engine.HasLayerSortMethod() {
		spxlog.Debug("Cannot manually set sprite layer when a layer sort mode is active.")
		return
	}
	if n == 0 {
		return
	}

	idx := s.findShapeIndex(spr)
	if idx < 0 {
		return
	}
	newIdx := s.calculateNewIndex(idx, n)
	if newIdx == idx {
		return
	}

	s.items = sliceutil.MoveToIndex(s.items, idx, newIdx)
	clear(s.named)
	s.updateRenderLayers()
}

// updateRenderLayers updates the layer index for all sprites.
func (s *shapeManager) updateRenderLayers() {
	if engine.HasLayerSortMethod() {
		return
	}

	layer := firstSpriteLayer
	for _, item := range s.items {
		if sp, ok := item.(*SpriteImpl); ok {
			sp.setLayer(layer)
			layer++
		}
	}
}

// all returns all active shapes.
func (s *shapeManager) all() []Shape {
	return s.items
}

// getTempShapes 复制活动列表，避免脚本在遍历中增删 Shape 破坏当前帧。
func (s *shapeManager) getTempShapes() []Shape {
	s.tempItems = sliceutil.CopyInto(s.tempItems, s.items, 50)
	return s.tempItems
}

// count returns the number of active shapes.
func (s *shapeManager) count() int {
	return len(s.items)
}

func (s *shapeManager) spritesNamed(name string) []*SpriteImpl {
	// 查询结果包含主实例与克隆；场景顺序变化时 add/remove 会清空缓存。
	if len(s.items) == 0 {
		return nil
	}
	if sprites, ok := s.named[name]; ok {
		return sprites
	}
	var sprites []*SpriteImpl
	for _, item := range s.items {
		if sp, ok := item.(*SpriteImpl); ok && sp.name == name {
			sprites = append(sprites, sp)
		}
	}
	if s.named == nil {
		s.named = make(map[string][]*SpriteImpl)
	} else if len(s.named) > len(s.items) {
		clear(s.named)
	}
	s.named[name] = sprites
	return sprites
}

// findSprite finds a sprite by name (only non-cloned sprites).
func (s *shapeManager) findSprite(name SpriteName) *SpriteImpl {
	for _, item := range s.items {
		if sp, ok := item.(*SpriteImpl); ok {
			if !sp.spriteState.Cloned && sp.name == name {
				return sp
			}
		}
	}
	return nil
}

// findShapeIndex finds the index of a shape in the items slice.
func (s *shapeManager) findShapeIndex(target Shape) int {
	for i, item := range s.items {
		if item == target {
			return i
		}
	}
	return -1
}

// calculateNewIndex calculates the new index after moving n sprite layers.
func (s *shapeManager) calculateNewIndex(currentIdx, n int) int {
	items := s.items
	newIdx := currentIdx

	if n > 0 {
		for newIdx > 0 && n > 0 {
			newIdx--
			if _, ok := items[newIdx].(*SpriteImpl); ok {
				n--
			}
		}
	} else if n < 0 {
		lastIdx := len(items) - 1
		for newIdx < lastIdx && n < 0 {
			newIdx++
			if _, ok := items[newIdx].(*SpriteImpl); ok {
				n++
			}
		}
	}

	return newIdx
}

// flushBubbleVisuals commits final bubble layout and UI state for the current frame.
func (s *shapeManager) flushBubbleVisuals(items []Shape) {
	s.layoutTextBubbles(items)

	delta := itime.DeltaTime()
	for _, item := range items {
		switch bubble := item.(type) {
		case *textBubble:
			bubble.onUpdate(delta)
		case *quoterBubble:
			bubble.onUpdate(delta)
		}
	}
}

func sortTextBubblesByLayoutID(bubbles []*textBubble) {
	// Bubble counts are normally tiny and already ordered. Insertion sort keeps
	// the unchanged-frame path allocation-free while making activation order
	// irrelevant to layout.
	for i := 1; i < len(bubbles); i++ {
		bubble := bubbles[i]
		j := i
		for j > 0 && bubbles[j-1].layoutID > bubble.layoutID {
			bubbles[j] = bubbles[j-1]
			j--
		}
		bubbles[j] = bubble
	}
}
