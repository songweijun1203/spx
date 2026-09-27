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

import (
	"sync/atomic"

	"github.com/goplus/spx/v3/internal/engine"
)

type BaseObjRuntimeState struct {
	// SyncSprite 是 SpriteImpl 对应的低层 Godot 同步代理，不是根包的 spx.Sprite。
	// 普通精灵路径下其动态对象为 *internal/engine.Sprite；代理通过 gid 与 Godot 节点关联。
	SyncSprite     *engine.Sprite
	Scale          float64
	IsCostumeSet   bool
	IsCostumeDirty bool
	Layer          int
	IsLayerDirty   bool
	HasShader      bool
	IsAnimating    bool
	hasDestroyed   atomic.Bool
}

func (s *BaseObjRuntimeState) IsDestroyed() bool {
	return s.hasDestroyed.Load()
}

func (s *BaseObjRuntimeState) MarkDestroyed() {
	s.hasDestroyed.Store(true)
}
