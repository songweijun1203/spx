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
	"maps"
	"reflect"

	corestate "github.com/goplus/spx/v3/internal/core/state"
	spxlog "github.com/goplus/spx/v3/internal/log"
)

// SpriteImpl 是根包 spx.Sprite 接口的具体游戏逻辑实现。
// 它被项目生成的精灵结构体嵌入；对应的 Godot 同步代理不在这里直接保存，
// 而是位于 baseObj.runtimeState.SyncSprite（*internal/engine.Sprite）。
type SpriteImpl struct {
	baseObj
	scriptEventBindings //脚本事件绑定

	sprite Sprite
	// original 指向克隆家族根；静态舞台实例即使来自同一模板也建立自己的家族。
	original    *SpriteImpl
	spriteState corestate.SpriteRuntimeState
	// proxyPublication points to fresh clone-local state. The pointer itself
	// remains stable after clone construction so reflective cloning never copies
	// publication state while it is in use.
	proxyPublication *cloneProxyPublication
	name             string
	components       spriteComponents

	g     *Game
	gamer reflect.Value
}

// -----------------------------------------------------------------------------
// Public API
// -----------------------------------------------------------------------------
func (p *SpriteImpl) Name() string {
	return p.name
}

func (p *SpriteImpl) IsCloned() bool {
	return p.spriteState.Cloned
}

// InitFrom 把源精灵的运行时基础状态复制到一个新 SpriteImpl，并重建克隆私有状态。
//
// 直接调用方：cloneSprite 通过反射查找生成精灵内嵌字段的 InitFrom 方法；总体流程
// 调用方：运行时 Clone。事件 registry 与源精灵共享，但 owner 改为新精灵，因此重跑
// Main 后产生的是克隆自己的 Sink；HasOnCloned 等“是否已注册”标志必须先清零。
// out.Set 的浅复制会暂时带入源 Godot 代理引用，随后 initRuntimeProxy 会将其清空并
// 新建独立代理，绝不能让原精灵和克隆体共同控制一个 SpxSprite 节点。
func (p *SpriteImpl) InitFrom(src *SpriteImpl) {
	p.baseObj.initFrom(&src.baseObj)
	p.scriptEventBindings.bind(src.scriptEventRegistry, p)

	p.g, p.name, p.runtimeState.Scale = src.g, src.name, src.runtimeState.Scale
	p.greffUniforms = maps.Clone(src.greffUniforms)

	p.spriteState.IsVisible = src.spriteState.IsVisible
	p.original = src.originalSprite()
	p.spriteState.Cloned = true
	p.spriteState.IsDying = false

	p.spriteState.DirtyVersion = 0
	p.spriteState.VisualVersion = 0
	p.spriteState.ProxySyncVersion = 0
	p.proxyPublication = nil
	p.spriteState.HasOnCloned = false
	p.spriteState.HasOnTouchStart = false
}

func (p *SpriteImpl) Die() {
	p.setDying()
	p.Stop(OtherScriptsInSprite)
	p.playStateAnimationAndWait(StateDie)
	p.Destroy()
}

func (p *SpriteImpl) Destroy() {
	if isDebugInstrEnabled() {
		spxlog.Debug("Destroy: %s", p.name)
	}
	p.destroy()
	p.Stop(ThisSprite)
	p.stopIfCurrentCoroutine()
}

func (p *SpriteImpl) DeleteThisClone() {
	if !p.spriteState.Cloned {
		return
	}
	p.Destroy()
}

// -----------------------------------------------------------------------------
// Internal State
// -----------------------------------------------------------------------------
func (p *SpriteImpl) setDying() {
	p.spriteState.IsDying = true
}

func (p *SpriteImpl) markProxyDirty() {
	p.markVisualDirty()
	p.spriteState.DirtyVersion++
}

func (p *SpriteImpl) markVisualDirty() {
	// 视觉版本通知相机/气泡等 Go 侧观察者；只有 markProxyDirty 还会递增代理版本。
	p.requestRedrawIfVisible()
	p.spriteState.VisualVersion++
}

// -----------------------------------------------------------------------------
// Components
// -----------------------------------------------------------------------------
func (p *SpriteImpl) transform() *transformComponent {
	return p.components.transform
}

func (p *SpriteImpl) animation() *animationComponent {
	return p.components.animation
}

func (p *SpriteImpl) physics() *physicsComponent {
	return p.components.physics
}

func (p *SpriteImpl) pen() *penComponent {
	return p.components.pen
}

func (p *SpriteImpl) sound() *soundComponent {
	return p.components.sound
}

func (p *SpriteImpl) bubble() *bubbleComponent {
	if p.components.bubble == nil {
		p.components.bubble = &bubbleComponent{sprite: p}
	}
	return p.components.bubble
}

// -----------------------------------------------------------------------------
// Lifecycle Helpers
// -----------------------------------------------------------------------------

func (p *SpriteImpl) playStateAnimationAndWait(stateName string) {
	animName := p.animation().getStateAnimName(stateName)
	if animName == "" || !p.animation().hasAnim(animName) {
		return
	}
	p.AnimateAndWait(animName)
}

// destroy releases resources without stopping scripts.
func (p *SpriteImpl) destroy() {
	if p.isDestroyed() {
		return
	}
	p.setVisible(false)
	p.clearHandlers()
	p.components.destroyComponents()
	p.g.shapeMgr.removeShape(p)
	if syncSprite := p.runtimeState.SyncSprite; syncSprite != nil {
		p.g.inputMgr.removeClickTarget(syncSprite.GetId())
	}
	p.markDestroyed()
}

func (p *SpriteImpl) stopIfCurrentCoroutine() {
	if gco.IsInCoroutine() {
		current := gco.Current()
		if current != nil && p == current.Obj {
			gco.StopCurrent()
		}
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------
var spriteImplType = reflect.TypeFor[SpriteImpl]()

func isSpriteBaseField(typ reflect.Type, index int) bool {
	return index >= 0 && index < typ.NumField() && typ.Field(index).Type == spriteImplType
}

func spriteOf(sprite Sprite) *SpriteImpl {
	vSpr := reflect.ValueOf(sprite)
	if vSpr.Kind() == reflect.Pointer {
		vSpr = vSpr.Elem()
	}
	if vSpr.Kind() != reflect.Struct {
		return nil
	}
	typ := vSpr.Type()
	for i := 0; i < typ.NumField(); i++ {
		if isSpriteBaseField(typ, i) {
			return vSpr.Field(i).Addr().Interface().(*SpriteImpl)
		}
	}
	return nil
}

// originalSprite returns the original instance, including for clones of clones.
// A configured stage instance starts its own family even if built from a template.
func (p *SpriteImpl) originalSprite() *SpriteImpl {
	if p.IsCloned() && p.original != nil {
		return p.original
	}
	return p
}
