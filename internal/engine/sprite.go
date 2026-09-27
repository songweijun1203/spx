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

//lint:file-ignore ST1001 Bridge code intentionally dot-imports mathf to mirror engine type names.

import (
	. "github.com/goplus/spbase/mathf"
	gdx "github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

// Sprite 是 internal/engine 层对 Godot SpxSprite 的具体同步代理。
// 它通过嵌入 gdx.Sprite 实现 gdx.ISpriter，并额外保存运行时资源信息和 Target。
// 它不是根包的 spx.Sprite 或 SpriteImpl；后两者属于游戏逻辑层。
// Sprite 的方法必须在引擎主线程执行。
type Sprite struct {
	gdx.Sprite
	Name    string
	PicPath string
	Target  any
}

// 编译期确认：内部同步代理通过嵌入 gdx.Sprite 实现低层精灵协议。
var _ gdx.ISpriter = (*Sprite)(nil)

func (s *Sprite) UpdateTexture(path string, renderScale float64, updateTexture bool) {
	if path == "" {
		return
	}
	s.PicPath = ToAssetPath(path)
	if updateTexture {
		s.SetTexture(s.PicPath)
	}
	s.SetRenderScale(UniformVec2(renderScale))
}

// UpdateTextureAtlas 设置图集区域；updateTexture=false 时只更新 Go 侧资源路径和
// 渲染缩放，实际纹理切换会由后续视觉同步或显式调用完成。
func (s *Sprite) UpdateTextureAtlas(path string, rect Rect2, renderScale float64, updateTexture bool) {
	if path == "" {
		return
	}
	s.PicPath = ToAssetPath(path)
	if updateTexture {
		s.SetTextureAtlas(s.PicPath, rect)
	}
	s.SetRenderScale(UniformVec2(renderScale))
}

func (s *Sprite) OnTriggerEnter(target gdx.ISpriter) {
	sprite, ok := target.(*Sprite)
	if ok {
		enqueueTriggerEvent(s, sprite)
	}
}

func (s *Sprite) RegisterOnAnimationLooped(fn func()) {
	s.Sprite.OnAnimationLoopedEvent.Subscribe(fn)
}

func (s *Sprite) UnRegisterOnAnimationLooped() {
	s.Sprite.OnAnimationLoopedEvent.UnsubscribeAll()
}

func (s *Sprite) RegisterOnAnimationFinished(fn func()) {
	s.Sprite.OnAnimationFinishedEvent.Subscribe(fn)
}

func (s *Sprite) UnRegisterOnAnimationFinished() {
	s.Sprite.OnAnimationFinishedEvent.UnsubscribeAll()
}

// Collider values use SPX coordinates.

func (s *Sprite) SetColliderShapeRect(trigger bool, center Vec2, size Vec2) {
	if trigger {
		s.Sprite.SetTriggerRect(center, size)
	} else {
		s.Sprite.SetColliderRect(center, size)
	}
}

func (s *Sprite) SetColliderShapeCircle(trigger bool, center Vec2, radius float64) {
	if trigger {
		s.Sprite.SetTriggerCircle(center, radius)
	} else {
		s.Sprite.SetColliderCircle(center, radius)
	}
}

func (s *Sprite) SetColliderShapeCapsule(trigger bool, center Vec2, size Vec2) {
	if trigger {
		s.Sprite.SetTriggerCapsule(center, size)
	} else {
		s.Sprite.SetColliderCapsule(center, size)
	}
}

func (s *Sprite) SetColliderShapePolygon(trigger bool, center Vec2, points []float64) {
	points32 := F64Tof32(points)
	if trigger {
		s.Sprite.SetTriggerPolygon(center, points32)
	} else {
		s.Sprite.SetColliderPolygon(center, points32)
	}
}

func (s *Sprite) SetColliderEnabled(trigger bool, enabled bool) {
	if trigger {
		s.Sprite.SetTriggerEnabled(enabled)
	} else {
		s.Sprite.SetCollisionEnabled(enabled)
	}
}
