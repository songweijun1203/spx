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

package state

type SpriteRuntimeState struct {
	IsVisible           bool   // Go 侧逻辑可见性，最终写入 Godot 根节点。
	Cloned              bool   // 是否为克隆体。
	IsDying             bool   // 是否进入销毁流程。
	IsDirty             bool   // 逻辑状态是否需要重新提交给运行时代理。
	DirtyVersion        uint64 // 每次逻辑变更递增，用于避免重复提交旧快照。
	ProxySyncVersion    uint64 // 最近一次成功同步到 Godot 代理的版本。
	VisualVersion       uint64 // 视觉资源/材质等独立更新版本。
	IsAwakened          bool   // 是否已完成 Go 侧生命周期初始化。
	HasOnCloned         bool   // 是否注册克隆回调。
	HasOnTouchStart     bool   // 是否注册触碰开始回调。
	HasOnTouching       bool   // 是否注册持续触碰回调。
	HasOnTouchEnd       bool   // 是否注册触碰结束回调。
	DefaultCostumeIndex int    // 默认服装索引。
}
