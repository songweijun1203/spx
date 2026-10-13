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

import "math"

const (
	SyncFieldsPerSprite     = 9  // 每个精灵更新占 9 个字段：id、位置、旋转、缩放、渲染偏移和可见性
	DefaultDeleteBufferSize = 16 // 待删除精灵 ID 切片的初始容量
)

// SpriteSyncData 保存一个精灵在本轮批量同步中需要发送给 Godot 的代理状态。
type SpriteSyncData struct {
	SpriteID      int64   // Godot 侧精灵对象的 ID，用于定位需要更新的节点
	X             float32 // 精灵根节点在 SPX 世界坐标系中的 X 坐标
	Y             float32 // 精灵根节点在 SPX 世界坐标系中的 Y 坐标
	Rotation      float32 // 发送给 Godot 的旋转角，单位为弧度
	ScaleX        float32 // 精灵根节点最终使用的 X 轴缩放
	ScaleY        float32 // 精灵根节点最终使用的 Y 轴缩放
	RenderOffsetX float32 // RenderRoot 相对精灵根节点的本地 X 偏移
	RenderOffsetY float32 // RenderRoot 相对精灵根节点的本地 Y 偏移
	Visible       float32 // 最终可见性；FFI 数据使用 1 表示可见、0 表示隐藏
}

// SpriteSyncBuffer 复用若干切片，完成精灵状态的批量下发和物理位置的批量回读。
type SpriteSyncBuffer struct {
	// data 保存本轮待发送的精灵变换和可见性记录。
	// flushSpriteProxyChanges 每轮开始时会清空长度，底层容量会保留复用。
	data []SpriteSyncData

	// deleteIDs 保存本轮需要在 Godot 侧销毁的精灵 ID。
	// Serialize 会将它们排列在所有更新记录之后，与更新记录一起批量发送。
	deleteIDs []int64

	// serialized 是 Serialize 使用的可复用 FFI 暂存区，格式为：
	// [更新数量, 删除数量, 更新记录..., 待删除 ID...]。
	// 返回的切片引用这块内存，调用方不能长期持有，也不能并发复用同一个 buffer。
	serialized []float32

	// positions 是从 Godot 批量回读物理位置时使用的可复用输出区。
	// 内容按传入 spriteIDs 的顺序排列为 [x0, y0, x1, y1, ...]，
	// 下一次 GetPositions 会覆盖其中的数据。
	positions []float32
}

// Add 只把一条精灵状态快照追加到 Go 侧缓冲区，不立即调用 Godot/ABI。
// 真正的跨语言调用发生在 Serialize 后的 SyncBatchUpdateSprites。
func (b *SpriteSyncBuffer) Add(id int64, x, y, rotation, scaleX, scaleY, renderOffsetX, renderOffsetY float64, visible bool) {
	vis := float32(0.0)
	if visible {
		vis = float32(1.0)
	}

	b.data = append(b.data, SpriteSyncData{
		SpriteID:      id,
		X:             float32(x),
		Y:             float32(y),
		Rotation:      float32(rotation),
		ScaleX:        float32(scaleX),
		ScaleY:        float32(scaleY),
		RenderOffsetX: float32(renderOffsetX),
		RenderOffsetY: float32(renderOffsetY),
		Visible:       vis,
	})
}

// AddDelete 记录本轮需要在 Godot 侧销毁的代理 ID。
func (b *SpriteSyncBuffer) AddDelete(id int64) {
	b.deleteIDs = append(b.deleteIDs, id)
}

// Clear 清空本轮内容但保留底层容量，避免每帧重新分配切片。
func (b *SpriteSyncBuffer) Clear() {
	b.data = b.data[:0]
	b.deleteIDs = b.deleteIDs[:0]
}

// UpdateCount 返回本轮变换更新数量。
func (b *SpriteSyncBuffer) UpdateCount() int {
	return len(b.data)
}

// DeleteCount 返回本轮待删除代理数量。
func (b *SpriteSyncBuffer) DeleteCount() int {
	return len(b.deleteIDs)
}

// GetDeleteIDs 返回待删除 ID；调用方通常只在提交后用于清理 Go 侧代理表。
func (b *SpriteSyncBuffer) GetDeleteIDs() []int64 {
	return b.deleteIDs
}

// Serialize 将缓冲区编码为给 C++/Web 桥接层使用的一维 float32 数据：
// [更新数量, 删除数量, 更新记录..., 删除 ID...]。
// 每条更新为 [id, x, y, rotation, scaleX, scaleY, offsetX, offsetY, visible]。
// Panics before returning a packet if an ID is negative or cannot be represented
// exactly in the legacy float32 format.
func (b *SpriteSyncBuffer) Serialize() []float32 {
	updateCount := len(b.data)
	deleteCount := len(b.deleteIDs)

	if updateCount == 0 && deleteCount == 0 {
		return nil
	}

	totalSize := 2 + updateCount*SyncFieldsPerSprite + deleteCount
	b.serialized = ensureFloat32BufferSize(b.serialized, totalSize)
	result := b.serialized

	result[0] = float32(updateCount)
	result[1] = float32(deleteCount)

	idx := 2

	for _, sprite := range b.data {
		result[idx] = encodeLegacyBatchObjectID(sprite.SpriteID)
		result[idx+1] = sprite.X
		result[idx+2] = sprite.Y
		result[idx+3] = sprite.Rotation
		result[idx+4] = sprite.ScaleX
		result[idx+5] = sprite.ScaleY
		result[idx+6] = sprite.RenderOffsetX
		result[idx+7] = sprite.RenderOffsetY
		result[idx+8] = sprite.Visible
		idx += SyncFieldsPerSprite
	}

	for _, id := range b.deleteIDs {
		result[idx] = encodeLegacyBatchObjectID(id)
		idx++
	}

	// The returned view is backed by reusable scratch storage and remains valid
	// only until the next buffer mutation.
	return result[:totalSize:totalSize]
}

// GetPositions 从 Godot 批量回读位置，结果为 [x0,y0,x1,y1,...]。
// 返回切片由本缓冲区复用，下一次调用会覆盖之前的数据。
func (b *SpriteSyncBuffer) GetPositions(spriteIDs []int64) []float32 {
	if len(spriteIDs) > math.MaxInt32/2 {
		panic("position batch exceeds the native array length limit")
	}
	b.positions = ensureFloat32BufferSize(b.positions, len(spriteIDs)*2)
	if len(spriteIDs) > 0 && !Managers().SpriteMgr.BatchRetrievePositions(spriteIDs, b.positions) {
		return nil
	}
	return b.positions
}

// NewSpriteSyncBuffer 创建可复用的精灵同步缓冲区。
func NewSpriteSyncBuffer(capacity int) *SpriteSyncBuffer {
	serializedCapacity := 2 + capacity*SyncFieldsPerSprite + DefaultDeleteBufferSize
	return &SpriteSyncBuffer{
		data:       make([]SpriteSyncData, 0, capacity),
		deleteIDs:  make([]int64, 0, DefaultDeleteBufferSize),
		serialized: make([]float32, 0, serializedCapacity),
	}
}

// SyncBatchUpdateSprites 是 Go -> Godot 的变换同步边界：一次 FFI 调用提交整批数据。
func SyncBatchUpdateSprites(buffer []float32) {
	if len(buffer) == 0 {
		return
	}

	Managers().SpriteMgr.BatchUpdateTransforms(buffer)
}
