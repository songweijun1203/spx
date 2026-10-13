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
	IsVisible           bool   // Go 侧逻辑可见性，最终结合克隆发布门控写入代理。
	Cloned              bool   // 是否为运行时克隆体。
	IsDying             bool   // 是否已经进入销毁流程。
	DirtyVersion        uint64 // 每次待同步代理变换递增。
	ProxySyncVersion    uint64 // 最近一次成功写入 Godot 代理的逻辑版本。
	VisualVersion       uint64 // 相机、气泡等 Go 侧视觉观察者使用的版本。
	HasOnCloned         bool   // 是否为当前 owner 注册了 OnCloned。
	HasOnTouchStart     bool   // 是否为当前 owner 注册了触碰开始处理器。
	DefaultCostumeIndex int    // 配置解析后的默认服装索引。
}
