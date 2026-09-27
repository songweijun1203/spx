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

package engine

const (
	// VisualFieldsPerSprite 是视觉批次中每个精灵占用的字段数：
	// [spriteId, renderScaleX, renderScaleY, zIndex, flags, uvX, uvY, uvW, uvH]。
	VisualFieldsPerSprite = 9

	VisualFlagHasZIndex  = 1 // bit 0: apply SetZIndex
	VisualFlagHasUvRemap = 2 // bit 1: apply SetMaterialParamsVec4 for UV remap
)

// VisualSyncData 保存与世界变换分开的视觉属性，例如渲染缩放、Z 序和图集 UV。
type VisualSyncData struct {
	SpriteID    int64
	RenderScale float32
	ZIndex      int32
	Flags       int32
	UvRemap     [4]float32 // x, y, w, h (UV remap for atlas textures)
}

// VisualSyncBuffer 收集视觉属性，格式与 SpriteSyncBuffer 类似但没有删除段。
// 当前常规服装更新仍主要走逐精灵接口；该缓冲区是独立的批量桥接能力。
type VisualSyncBuffer struct {
	data       []VisualSyncData
	serialized []float32
}

// AddRenderScale 追加仅渲染缩放更新。
func (b *VisualSyncBuffer) AddRenderScale(id int64, renderScale float64) {
	b.data = append(b.data, VisualSyncData{
		SpriteID:    id,
		RenderScale: float32(renderScale),
	})
}

// AddFull 追加完整视觉更新，可选择附带 Z 序和图集 UV 重映射。
func (b *VisualSyncBuffer) AddFull(id int64, renderScale float64, zIndex int, hasZIndex bool, uvRemap [4]float64, hasUvRemap bool) {
	entry := VisualSyncData{
		SpriteID:    id,
		RenderScale: float32(renderScale),
		ZIndex:      int32(zIndex),
	}
	if hasZIndex {
		entry.Flags |= VisualFlagHasZIndex
	}
	if hasUvRemap {
		entry.Flags |= VisualFlagHasUvRemap
		entry.UvRemap = [4]float32{
			float32(uvRemap[0]), float32(uvRemap[1]),
			float32(uvRemap[2]), float32(uvRemap[3]),
		}
	}
	b.data = append(b.data, entry)
}

// Clear 清空本轮视觉更新并保留容量。
func (b *VisualSyncBuffer) Clear() {
	b.data = b.data[:0]
}

// Count 返回本轮视觉更新数量。
func (b *VisualSyncBuffer) Count() int {
	return len(b.data)
}

// Serialize 将视觉更新编码成 FFI 一维数组：
// [count, entry0..., entry1...]，每条 entry 为
// [spriteId, renderScaleX, renderScaleY, zIndex, flags, uvX, uvY, uvW, uvH]。
// Panics before returning a packet if an ID is negative or cannot be represented
// exactly in the legacy float32 format.
func (b *VisualSyncBuffer) Serialize() []float32 {
	count := len(b.data)
	if count == 0 {
		return nil
	}

	totalSize := 1 + count*VisualFieldsPerSprite
	b.serialized = ensureFloat32BufferSize(b.serialized, totalSize)
	result := b.serialized

	result[0] = float32(count)
	idx := 1

	for _, entry := range b.data {
		result[idx] = encodeLegacyBatchObjectID(entry.SpriteID)
		result[idx+1] = entry.RenderScale
		result[idx+2] = entry.RenderScale // scaleX == scaleY for render scale
		result[idx+3] = float32(entry.ZIndex)
		result[idx+4] = float32(entry.Flags)
		result[idx+5] = entry.UvRemap[0]
		result[idx+6] = entry.UvRemap[1]
		result[idx+7] = entry.UvRemap[2]
		result[idx+8] = entry.UvRemap[3]
		idx += VisualFieldsPerSprite
	}

	// The returned view is backed by reusable scratch storage and remains valid
	// only until the next buffer mutation.
	return result[:totalSize:totalSize]
}

// NewVisualSyncBuffer 创建可复用的视觉同步缓冲区。
func NewVisualSyncBuffer(capacity int) *VisualSyncBuffer {
	return &VisualSyncBuffer{
		data:       make([]VisualSyncData, 0, capacity),
		serialized: make([]float32, 0, 1+capacity*VisualFieldsPerSprite),
	}
}

// SyncBatchUpdateVisuals 是独立的 Go -> Godot 视觉批次同步边界。
func SyncBatchUpdateVisuals(buffer []float32) {
	if len(buffer) == 0 {
		return
	}
	Managers().SpriteMgr.BatchUpdateVisuals(buffer)
}
