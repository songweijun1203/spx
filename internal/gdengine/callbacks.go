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

package gdengine

//lint:file-ignore ST1001 Godot callback glue intentionally dot-imports engine API types.

import (
	spxlog "github.com/goplus/spx/v3/internal/log"
	itime "github.com/goplus/spx/v3/internal/time"
	. "github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

// bindCallbacks 组装平台无关的 Go 回调表。
//
// 平台：公共层，Native 与 Web 共用同一套业务回调。
// 调用时机：PrepareLink() 已建立平台 FFI、即将创建 Manager 时。
// 直接上级：gdengine.PrepareLink()。
// 跨模块使用：Native 由 func_on_xxx 调用，Web 由 gdspxDispatch() 调用；
// 两条平台链路最终都会进入本文件的 onXxx 函数。
func bindCallbacks() CallbackInfo {
	return CallbackInfo{
		CoreCallbackInfo: CoreCallbackInfo{
			OnEngineStart:       onEngineStart,
			OnEngineUpdate:      onEngineUpdate,
			OnEngineFixedUpdate: onEngineFixedUpdate,
			OnEngineDestroy:     onEngineDestroy,
			OnEngineDestroyed:   onEngineDestroyed,
			OnEngineReset:       onEngineReset,
			OnEnginePause:       onEnginePause,
			OnMousePressed:      onMousePressed,
			OnMouseReleased:     onMouseReleased,
			OnKeyPressed:        onKeyPressed,
			OnKeyReleased:       onKeyReleased,
		},

		OnSceneSpriteInstantiated: onSceneSpriteInstantiated,
		OnSpriteReady:             onSpriteReady,
		OnSpriteUpdated:           onSpriteUpdated,
		OnSpriteFixedUpdated:      onSpriteFixedUpdated,
		OnSpriteDestroyed:         onSpriteDestroyed,
		OnSpriteScreenEntered:     onSpriteScreenEntered,
		OnSpriteScreenExited:      onSpriteScreenExited,
		OnSpriteVfxFinished:       onSpriteVfxFinished,
		OnSpriteAnimationFinished: onSpriteAnimationFinished,
		OnSpriteAnimationLooped:   onSpriteAnimationLooped,
		OnSpriteFrameChanged:      onSpriteFrameChanged,
		OnSpriteAnimationChanged:  onSpriteAnimationChanged,
		OnSpriteFramesSetChanged:  onSpriteFramesSetChanged,

		OnActionPressed:      onActionPressed,
		OnActionJustPressed:  onActionJustPressed,
		OnActionJustReleased: onActionJustReleased,
		OnAxisChanged:        onAxisChanged,

		OnCollisionEnter: onCollisionEnter,
		OnCollisionStay:  onCollisionStay,
		OnCollisionExit:  onCollisionExit,
		OnTriggerEnter:   onTriggerEnter,
		OnTriggerStay:    onTriggerStay,
		OnTriggerExit:    onTriggerExit,

		// UI 的 OnStart 由 Go 创建流程处理；UI 更新回调本身没有 delta 参数。
		OnUiDestroyed:   onUiDestroyed,
		OnUiPressed:     onUiPressed,
		OnUiReleased:    onUiReleased,
		OnUiHovered:     onUiHovered,
		OnUiClicked:     onUiClicked,
		OnUiToggle:      onUiToggle,
		OnUiTextChanged: onUiTextChanged,
	}
}

// 以下 onEngineXxx 是 Native/Web 共用的引擎生命周期处理函数。
// 直接上级：bindCallbacks() 返回的 CallbackInfo；平台 FFI 收到 Godot 事件后调用。

// onEngineStart 先启动所有 Manager，再把启动事件交给 internal/engine。
func onEngineStart() {
	for _, mgr := range mgrs {
		mgr.OnStart()
	}
	if coreCallbacks.OnEngineStart != nil {
		coreCallbacks.OnEngineStart()
	}
}

// onEngineUpdate 处理每帧 Manager、精灵和游戏运行时更新。
func onEngineUpdate(delta float64) {
	// 输入回放期间统一使用 SPX 固定逻辑步长，使脚本、计时器、Tween 和引擎更新保持一致。
	delta = itime.EffectiveLogicalDeltaTime(delta)
	for _, mgr := range mgrs {
		mgr.OnUpdate(delta)
	}
	AdvanceTimeSinceGameStart(delta)
	sprites = sprites[:0]
	for _, sprite := range Sprites() {
		sprites = append(sprites, sprite)
	}
	for _, sprite := range sprites {
		sprite.OnUpdate(delta)
	}
	if coreCallbacks.OnEngineUpdate != nil {
		coreCallbacks.OnEngineUpdate(delta)
	}
	InternalUpdateEngine(delta)
}

// onEngineFixedUpdate 处理 Godot 物理帧对应的 Go Manager 和精灵钩子。
//
// 它的直接上级是平台 FFI 转发的 OnEngineFixedUpdate，最顶层来源是
// Godot 的固定物理帧。这里不负责替代 Godot PhysicsServer 的碰撞求解，
// 也不调用普通逻辑帧的 Game.OnEngineUpdate() 或 gco.Update()；它只把
// 固定 delta 交给 Go Manager 和每个精灵的 OnFixedUpdate()。
// 默认 Manager/Sprite 的 OnFixedUpdate 是空实现，项目或扩展只有在需要
// 固定步长逻辑时覆盖它，才会在这里执行实际代码。
func onEngineFixedUpdate(delta float64) {
	// 固定更新保留 Godot 原始物理 delta；输入回放只虚拟化 SPX 的逻辑更新时间。
	for _, mgr := range mgrs {
		mgr.OnFixedUpdate(delta)
	}
	// 复制当前精灵列表，避免用户固定帧回调中创建/销毁精灵时影响本轮遍历。
	sprites = sprites[:0]
	for _, sprite := range Sprites() {
		sprites = append(sprites, sprite)
	}
	for _, sprite := range sprites {
		sprite.OnFixedUpdate(delta)
	}
	if coreCallbacks.OnEngineFixedUpdate != nil {
		coreCallbacks.OnEngineFixedUpdate(delta)
	}
}

// onEngineDestroy 按“游戏逻辑、精灵、Manager”的顺序执行销毁前处理。
func onEngineDestroy() {
	if coreCallbacks.OnEngineDestroy != nil {
		coreCallbacks.OnEngineDestroy()
	}
	sprites = sprites[:0]
	for _, sprite := range Sprites() {
		sprites = append(sprites, sprite)
	}
	for _, sprite := range sprites {
		sprite.OnDestroy()
	}
	for _, mgr := range mgrs {
		mgr.OnDestroy()
	}
}

func onEngineDestroyed() {
	if coreCallbacks.OnEngineDestroyed != nil {
		coreCallbacks.OnEngineDestroyed()
	}
}

func onEngineReset() {
	if coreCallbacks.OnEngineReset != nil {
		coreCallbacks.OnEngineReset()
	}
}

func onEnginePause(isPaused bool) {
	if coreCallbacks.OnEnginePause != nil {
		coreCallbacks.OnEnginePause(isPaused)
	}

	for _, mgr := range mgrs {
		mgr.OnPause(isPaused)
	}
}

func onSceneSpriteInstantiated(id int64, type_name string) {
	BindSceneInstantiatedSprite(Object(id), type_name)
}

// 以下为 Native/Web 共用的精灵事件处理函数。
func onSpriteReady(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.OnStart()
	}
}

func onSpriteUpdated(delta float64) {
	spxlog.Debug("OnSpriteUpdated %f", delta)
}

func onSpriteFixedUpdated(delta float64) {
	spxlog.Debug("OnSpriteFixedUpdated %f", delta)
}

func onSpriteDestroyed(id int64) {
	DeleteSprite(Object(id))
}

// 以下为 Native/Web 共用的输入事件处理函数。
func onMousePressed(id int64) {
	spxlog.Debug("OnMousePressed %d", id)
	if coreCallbacks.OnMousePressed != nil {
		coreCallbacks.OnMousePressed(id)
	}
}

func onMouseReleased(id int64) {
	spxlog.Debug("OnMouseReleased %d", id)
	if coreCallbacks.OnMouseReleased != nil {
		coreCallbacks.OnMouseReleased(id)
	}
}

func onKeyPressed(id int64) {
	spxlog.Debug("OnKeyPressed %d", id)
	if coreCallbacks.OnKeyPressed != nil {
		coreCallbacks.OnKeyPressed(id)
	}
}

func onKeyReleased(id int64) {
	spxlog.Debug("OnKeyReleased %d", id)
	if coreCallbacks.OnKeyReleased != nil {
		coreCallbacks.OnKeyReleased(id)
	}
}

func onActionPressed(name string) {
	spxlog.Debug("OnActionPressed %s", name)
}

func onActionJustPressed(name string) {
	spxlog.Debug("OnActionJustPressed %s", name)
}

func onActionJustReleased(name string) {
	spxlog.Debug("OnActionJustReleased %s", name)
}

func onAxisChanged(name string, value float64) {
	spxlog.Debug("OnAxisChanged %s %f", name, value)
}

// 以下为 Native/Web 共用的碰撞和触发事件处理函数。
func onCollisionEnter(id int64, oid int64) {
	spxlog.Debug("OnCollisionEnter %d %d", id, oid)
}

func onCollisionStay(id int64, oid int64) {
	spxlog.Debug("OnCollisionStay %d %d", id, oid)
}

func onCollisionExit(id int64, oid int64) {
	spxlog.Debug("OnCollisionExit %d %d", id, oid)
}

func onTriggerEnter(id int64, oid int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		if other := GetSprite(Object(oid)); other != nil {
			sprite.V_OnTriggerEnter(other)
			sprite.OnTriggerEnter(other)
		}
	}
}

func onTriggerStay(id int64, oid int64) {
}

func onTriggerExit(id int64, oid int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		if other := GetSprite(Object(oid)); other != nil {
			sprite.V_OnTriggerExit(other)
			sprite.OnTriggerExit(other)
		}
	}
}

// 以下为 Native/Web 共用的 UI 事件处理函数。
func onUiPressed(id int64) {
	if node := GetUINode(Object(id)); node != nil {
		node.V_OnUiPressed()
		node.OnUiPressed()
	}
}

func onUiReleased(id int64) {
	if node := GetUINode(Object(id)); node != nil {
		node.V_OnUiReleased()
		node.OnUiReleased()
	}
}

func onUiHovered(id int64) {
	if node := GetUINode(Object(id)); node != nil {
		node.V_OnUiHovered()
		node.OnUiHovered()
	}
}

func onUiClicked(id int64) {
	if node := GetUINode(Object(id)); node != nil {
		node.V_OnUiClick()
		node.OnUiClick()
	}
}

func onUiToggle(id int64, isOn bool) {
	if node := GetUINode(Object(id)); node != nil {
		node.V_OnUiToggle(isOn)
		node.OnUiToggle(isOn)
	}
}

func onUiTextChanged(id int64, text string) {
	if node := GetUINode(Object(id)); node != nil {
		node.V_OnUiTextChanged(text)
		node.OnUiTextChanged(text)
	}
}

func onUiDestroyed(id int64) {
	DeleteUINode(Object(id))
}

func onSpriteScreenEntered(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.V_OnScreenEntered()
		sprite.OnScreenEntered()
	}
}

func onSpriteScreenExited(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.V_OnScreenExited()
		sprite.OnScreenExited()
	}
}

func onSpriteVfxFinished(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.V_OnVfxFinished()
		sprite.OnVfxFinished()
	}
}

func onSpriteAnimationFinished(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.V_OnAnimationFinished()
		sprite.OnAnimationFinished()
	}
}

func onSpriteAnimationLooped(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.V_OnAnimationLooped()
		sprite.OnAnimationLooped()
	}
}

func onSpriteFrameChanged(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.V_OnFrameChanged()
		sprite.OnFrameChanged()
	}
}

func onSpriteAnimationChanged(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.V_OnAnimationChanged()
		sprite.OnAnimationChanged()
	}
}

func onSpriteFramesSetChanged(id int64) {
	if sprite := GetSprite(Object(id)); sprite != nil {
		sprite.V_OnFramesSetChanged()
		sprite.OnFramesSetChanged()
	}
}
