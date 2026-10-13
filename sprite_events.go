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
	"slices"

	coreevent "github.com/goplus/spx/v3/internal/core/event"
)

// OnCloned__0 注册不接收克隆数据的处理器。
//
// 直接调用方：生成精灵的 Main；总体流程调用方：cloneSprite 重跑克隆对象 Main 时
// 建立其独立事件注册。这里仅注册，不会立即执行处理器。
func (p *SpriteImpl) OnCloned__0(onCloned func()) {
	p.OnCloned__1(coreevent.Ignore1[any](onCloned))
}

// OnCloned__1 把处理器登记到共享事件中心，并以当前 SpriteImpl 作为 owner。
//
// 直接调用方：OnCloned__0 或生成代码中携带 data 的 OnCloned 注册；总体流程调用方：
// 克隆对象的 Main。HasOnCloned 让 dispatchCloneLifecycle 在没有处理器时跳过事件快照。
func (p *SpriteImpl) OnCloned__1(onCloned func(data any)) {
	p.spriteState.HasOnCloned = true
	p.scriptEventRegistry.manager.Add(coreevent.BucketCloned, coreevent.NewSink(p, onCloned, coreevent.MatchOwner(p)))
}

func (p *SpriteImpl) OnTouchStart__0(sprite SpriteName, onTouchStart func()) {
	p.OnTouchStart__1(sprite, coreevent.Ignore1[Sprite](onTouchStart))
}

func (p *SpriteImpl) OnTouchStart__1(sprite SpriteName, onTouchStart func(Sprite)) {
	p.physics().addCollisionTarget(sprite)
	p.addTouchStartHandler(func(s Sprite) {
		impl := spriteOf(s)
		if impl != nil && impl.name == sprite {
			onTouchStart(s)
		}
	})
}

func (p *SpriteImpl) OnTouchStart__2(sprites []SpriteName, onTouchStart func()) {
	p.OnTouchStart__3(sprites, coreevent.Ignore1[Sprite](onTouchStart))
}

func (p *SpriteImpl) OnTouchStart__3(sprites []SpriteName, onTouchStart func(Sprite)) {
	for _, sprite := range sprites {
		p.physics().addCollisionTarget(sprite)
	}
	p.addTouchStartHandler(func(s Sprite) {
		impl := spriteOf(s)
		if impl != nil && slices.Contains(sprites, impl.name) {
			onTouchStart(s)
		}
	})
}

func (p *SpriteImpl) fireTouchStart(obj *SpriteImpl) {
	if p.spriteState.HasOnTouchStart {
		p.doWhenTouchStart(p, obj)
	}
}

func (p *SpriteImpl) addTouchStartHandler(onTouchStart func(Sprite)) {
	p.spriteState.HasOnTouchStart = true
	p.scriptEventRegistry.manager.Add(coreevent.BucketTouchStart, coreevent.NewSink(p, onTouchStart, coreevent.MatchOwner(p)))
}
