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
	"reflect"
	"unsafe"

	coreproject "github.com/goplus/spx/v3/internal/core/project"
	spxlog "github.com/goplus/spx/v3/internal/log"
)

var spriteImplType = reflect.TypeOf(SpriteImpl{})

func (p *SpriteImpl) Clone__0() {
	p.CloneWith(nil)
}

func (p *SpriteImpl) Clone__1(data any) {
	p.CloneWith(data)
}

func (p *SpriteImpl) CloneWith(__xgo_optional_data any) {
	doClone(p.sprite, __xgo_optional_data, nil)
}

// TODO(xsw): use classfile clone mechanism instead of reflection.
func doClone(sprite Sprite, data any, onCloned func(sprite *SpriteImpl)) {
	if sprite == nil {
		spxlog.Panicf("DoClone: sprite is nil")
	}
	src := spriteOf(sprite)
	// 第一步只创建运行时克隆对象和它的原生代理。createRuntimeClone 内部会
	// 重新执行克隆的 Main，让 OnCloned 等语句为新 owner 登记 event sink。
	dest := createRuntimeClone(src)
	if dest == nil {
		return
	}
	dest.requestRedrawIfVisible()
	if onCloned != nil {
		onCloned(dest)
	}
	// 第二步才触发 clone 生命周期事件。匹配到的每个 OnCloned sink 会由
	// StartBatch 创建独立 Thread；不是在当前 Ground/创建者协程里直接调用。
	dispatchCloneLifecycle(dest, data)
}

func createRuntimeClone(src *SpriteImpl) *SpriteImpl {
	shapes := &src.g.shapeMgr
	if !shapes.reserveClone() {
		return nil
	}
	defer func() { shapes.pendingClones-- }()

	if isDebugInstrEnabled() {
		spxlog.Debug("Clone: %s", src.name)
	}
	in := reflect.ValueOf(src.sprite).Elem()
	v := reflect.New(in.Type())
	out, outPtr := v.Elem(), v.Interface().(Sprite)
	dest := cloneSprite(out, outPtr, in, nil)
	src.g.addClonedShape(src, dest)
	return dest
}

func cloneSprite(out reflect.Value, outPtr Sprite, in reflect.Value, v coreproject.StageShape) *SpriteImpl {
	dest := spriteOf(outPtr)
	func() {
		out.Set(in)
		for i, n := 0, out.NumField(); i < n; i++ {
			dstField := settableSpriteField(out.Field(i))
			srcField := settableSpriteField(in.Field(i))
			if !dstField.IsValid() || !srcField.IsValid() {
				continue
			}
			if ini := dstField.Addr().MethodByName("InitFrom"); ini.IsValid() {
				ini.Call([]reflect.Value{srcField.Addr()})
			}
		}
	}()
	dest.sprite = outPtr
	dest.runtimeState.IsCostumeDirty = true
	// The clone gets a fresh engine proxy, so its copied layer must be pushed
	// even when it is numerically unchanged from the source layer.
	dest.runtimeState.IsLayerDirty = true

	src := spriteOf(in.Addr().Interface().(Sprite))
	dest.components.cloneFrom(&src.components, dest)

	if v != nil {
		applySpriteProps(dest, v)
	} else {
		// The native proxy must stay hidden until the clone's initialization
		// handlers have completed their first execution slice.
		dest.beginCloneProxyPublication()
	}
	dest.initRuntimeProxy()
	if v == nil {
		dest.awake()
		// Re-running Main re-registers clone events but also replays XGo_Init.
		// Save top-level user fields first, then restore them without changing
		// the existing out.Set(in) reference semantics.
		userState := snapshotSpriteUserFields(out)
		// Main 在这里的职责是为克隆对象重新登记事件 handler。此时仅产生
		// event sink，不等于 OnCloned 已经运行，也还没有对应 Thread ID。
		runMain(outPtr.Main)
		restoreSpriteUserFields(out, userState)
	}
	return dest
}

func snapshotSpriteUserFields(v reflect.Value) map[int]reflect.Value {
	out := make(map[int]reflect.Value, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		fieldType := v.Type().Field(i).Type
		if fieldType == spriteImplType {
			continue
		}
		field := settableSpriteField(v.Field(i))
		if !field.IsValid() {
			continue
		}
		saved := reflect.New(field.Type()).Elem()
		saved.Set(field)
		out[i] = saved
	}
	return out
}

func restoreSpriteUserFields(v reflect.Value, state map[int]reflect.Value) {
	for i, saved := range state {
		field := settableSpriteField(v.Field(i))
		if !field.IsValid() {
			continue
		}
		field.Set(saved)
	}
}

func settableSpriteField(field reflect.Value) reflect.Value {
	if field.CanSet() {
		return field
	}
	if !field.CanAddr() {
		return reflect.Value{}
	}
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
}

func dispatchCloneLifecycle(dest *SpriteImpl, data any) {
	// doWhenCloned 使用 BatchWaitFirstSlice。若处理函数是：
	//
	//   setCostume; setGraphicEffect; wait 0.01; deleteThisClone
	//
	// 第一次执行片段会完成 setCostume/setGraphicEffect，并在 wait 处 Yield；
	// 创建者随后才能离开克隆初始化。下一帧从 wait 后恢复并执行删除的，仍是
	// 同一个 OnCloned Thread，而不是第二次触发 OnCloned。
	defer dest.finishCloneInitialization()
	if dest.spriteState.HasOnCloned {
		dest.doWhenCloned(dest, data)
	}
}

func applySpriteProps(dest *SpriteImpl, v coreproject.StageShape) {
	transform := dest.transform()
	if x, ok := v["x"]; ok {
		transform.x = x.(float64)
	}
	if y, ok := v["y"]; ok {
		transform.y = y.(float64)
	}
	if heading, ok := v["heading"]; ok {
		transform.direction = heading.(float64)
	}
	if style, ok := v["rotationStyle"]; ok {
		transform.rotationStyle = toRotationStyle(style.(string))
	}
	if visible, ok := v["visible"]; ok {
		dest.spriteState.IsVisible = visible.(bool)
	}
	if size, ok := v["size"]; ok {
		dest.runtimeState.Scale = size.(float64)
	}
	if idx, ok := v["costumeIndex"]; ok {
		dest.setCostumeIndex(int(idx.(float64)))
	}
	dest.spriteState.Cloned = false
}

func applySprite(out reflect.Value, sprite Sprite, v coreproject.StageShape) (*SpriteImpl, Sprite) {
	in := reflect.ValueOf(sprite).Elem()
	outPtr := out.Addr().Interface().(Sprite)
	return cloneSprite(out, outPtr, in, v), outPtr
}
