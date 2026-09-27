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
	"sync/atomic"

	"github.com/goplus/spx/v3/internal/base/sliceutil"
	"github.com/goplus/spx/v3/internal/engine"
	spxlog "github.com/goplus/spx/v3/internal/log"
	itime "github.com/goplus/spx/v3/internal/time"
	"github.com/goplus/spx/v3/internal/ui"
)

// 画笔使用独立的第 0 层：位于背景之上、所有受管精灵之下。
// 业务精灵从 firstSpriteLayer 开始，避免 Z 序重建时占用共享画布层。
const (
	penLayer         = 0
	firstSpriteLayer = penLayer + 1
)

// 所有精灵共享同一个克隆上限，包括尚未公开显示的克隆。
const maxClones = 300

// shapeManager 管理一局游戏中所有 Shape 的业务生命周期。
// items 是当前活动对象的权威顺序；destroyItems 保存等待帧末同步删除的对象；
// tempItems 供逐帧扫描复用。它还负责克隆计数、渲染层重排和气泡布局缓存。
type shapeManager struct {
	cloneCount               int
	pendingClones            int
	items                    []Shape
	tempItems                []Shape
	destroyItems             []Shape
	textBubbles              []*textBubble
	activeTextBubbles        []*textBubble
	sayLayouts               []ui.SayBubbleLayout
	nextTextBubbleLayoutID   uint64
	pendingClonePublications atomic.Bool
}

// init 为新一局清空状态，并尽量复用上一局已经分配的切片容量。
func (s *shapeManager) init() {
	s.cloneCount = 0
	s.pendingClones = 0
	s.pendingClonePublications.Store(false)
	if s.items == nil {
		s.items = make([]Shape, 0, 64)
	} else {
		s.items = s.items[:0]
	}
	if s.tempItems == nil {
		s.tempItems = make([]Shape, 0, 50)
	} else {
		s.tempItems = s.tempItems[:0]
	}
	if s.destroyItems == nil {
		s.destroyItems = make([]Shape, 0, 16)
	} else {
		s.destroyItems = s.destroyItems[:0]
	}
	clear(s.textBubbles)
	s.textBubbles = s.textBubbles[:0]
	clear(s.activeTextBubbles)
	s.activeTextBubbles = s.activeTextBubbles[:0]
	clear(s.sayLayouts)
	s.sayLayouts = s.sayLayouts[:0]
	s.nextTextBubbleLayoutID = 0
}

// markCloneProxyPublicationReady 记录至少一个克隆已经完成首段初始化。
//
// 直接调用方：SpriteImpl.finishCloneInitialization；总体流程调用方：OnCloned 首段结束。
// 它只发出帧边界通知，不直接显示 Godot 节点；真正公开由代理收集处理。
func (s *shapeManager) markCloneProxyPublicationReady() {
	s.pendingClonePublications.Store(true)
}

// takeCloneProxyPublications 在渲染前消费克隆就绪通知。
//
// 直接调用方：Game.OnEngineRender；总体流程调用方：Godot 每帧 update 的渲染准备阶段。
// 随后的 syncPostCoroutineVisuals 会扫描所有精灵，由 collectProxyUpdate 将 Ready 克隆
// 推进为 Published，并把最终服装、图层、变换和可见性提交给 Godot。
func (s *shapeManager) takeCloneProxyPublications() bool {
	return s.pendingClonePublications.Swap(false)
}

// reset 先清除所有 Godot 精灵代理，再清空 Go 侧 Shape 状态；用于场景/局次切换。
func (s *shapeManager) reset() {
	engine.ClearAllSprites()
	s.init()
}

// flushActivate 推进当前帧中需要独立更新的非精灵、非气泡 Shape。
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
	clear(s.textBubbles)
	s.textBubbles = s.textBubbles[:0]
	clear(s.sayLayouts)
	s.sayLayouts = s.sayLayouts[:0]

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
		clear(s.activeTextBubbles)
		s.activeTextBubbles = append(s.activeTextBubbles[:0], s.textBubbles...)
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

	s.destroyItems = s.destroyItems[:0]
}

// add 立即把 Shape 加入活动列表；克隆和文字气泡还会维护各自的附加状态。
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
}

// remove 只登记延迟销毁；真正通知 Godot 删除发生在帧同步的 flushDestroy。
func (s *shapeManager) remove(shape Shape) {
	s.destroyItems = append(s.destroyItems, shape)
}

// addShape 是 Game 添加普通 Shape 时使用的统一入口。
func (s *shapeManager) addShape(child Shape) {
	s.add(child)
}

// addClonedShape 把克隆立即插到源对象后方，并更新活动对象和渲染层顺序。
//
// 直接调用方：Game.addClonedShape；总体流程调用方：createRuntimeClone。这样符合
// Scratch 的层级语义：同一对象的克隆按从旧到新排列，原对象仍位于全部克隆之前。
// 插入发生在 OnCloned 派发之前，因此处理器查询、移动或删除克隆时已经能找到该对象。
func (s *shapeManager) addClonedShape(src, clone Shape) {
	idx := s.findShapeIndex(src)
	if idx < 0 {
		spxlog.Debug("AddClonedShape: cloning a deleted sprite")
		gco.StopCurrent()
		return
	}

	s.items = sliceutil.InsertAt(s.items, idx, clone)
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

// removeShape removes a shape from the active list and schedules it for destruction.
func (s *shapeManager) removeShape(child Shape) {
	idx := s.findShapeIndex(child)
	if idx < 0 {
		return
	}

	s.items = sliceutil.DeleteAt(s.items, idx)
	if sprite, ok := child.(*SpriteImpl); ok && sprite.IsCloned() {
		s.cloneCount--
	}
	s.remove(child)
	s.updateRenderLayers()
}

// activateShape moves a shape to the end of the active list.
func (s *shapeManager) activateShape(child Shape) {
	items := s.items
	for idx, item := range items {
		if item == child {
			if idx == len(items)-1 {
				return
			}
			s.items = sliceutil.MoveToEnd(s.items, idx)
			s.updateRenderLayers()
			return
		}
	}
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

// getTempShapes returns a copy of all active shapes in a temporary buffer.
func (s *shapeManager) getTempShapes() []Shape {
	s.tempItems = sliceutil.CopyInto(s.tempItems, s.items, 50)
	return s.tempItems
}

// count returns the number of active shapes.
func (s *shapeManager) count() int {
	return len(s.items)
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
