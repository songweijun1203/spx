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

	spxlog "github.com/goplus/spx/v3/internal/log"
)

func (p *SpriteImpl) Clone__0() {
	p.CloneWith(nil)
}

func (p *SpriteImpl) Clone__1(data any) {
	p.CloneWith(data)
}

func (p *SpriteImpl) CloneWith(__xgo_optional_data any) {
	doClone(p.sprite, __xgo_optional_data, nil)
}

// doClone 创建运行时克隆，允许内部调用方在 OnCloned 前补充初始化。
// 生命周期处理器至少运行到首次挂起或结束后，代理才进入可公开状态。
// TODO(xsw): use classfile clone mechanism instead of reflection.
func doClone(sprite Sprite, data any, onCloned func(sprite *SpriteImpl)) {
	if sprite == nil {
		spxlog.Panicf("DoClone: sprite is nil")
	}
	src := spriteOf(sprite)
	dest := createRuntimeClone(src)
	if dest == nil {
		return
	}
	dest.requestRedrawIfVisible()
	if onCloned != nil {
		onCloned(dest)
	}
	dispatchCloneLifecycle(dest, data)
}

func createRuntimeClone(src *SpriteImpl) *SpriteImpl {
	// 先预占额度，防止克隆初始化期间的递归 Clone 越过全局上限。
	shapes := &src.g.shapeMgr
	if !shapes.reserveClone() {
		return nil
	}
	defer func() { shapes.pendingClones-- }()

	if isDebugInstrEnabled() {
		spxlog.Debug("Clone: %s", src.name)
	}
	// 使用业务精灵的实际生成类型创建可寻址结构体，再逐字段复制运行时状态。
	out := reflect.New(reflect.TypeOf(src.sprite).Elem()).Elem()
	dest := instantiateRuntimeClone(out, src.sprite)
	src.g.shapeMgr.addClonedShape(src, dest)
	return dest
}

func instantiateRuntimeClone(out reflect.Value, source Sprite) *SpriteImpl {
	dest, outPtr := copySprite(out, source)
	// 代理从创建开始保持隐藏，直到 Main 与 OnCloned 的首段初始化完成。
	dest.beginCloneProxyPublication()
	dest.initRuntimeProxy()
	dest.awake()

	// 重跑 Main 会为克隆登记独立事件，但也会重放 XGo_Init；保存并恢复顶层
	// 用户字段，保留 copySprite 的浅复制引用语义。
	userState := snapshotSpriteUserFields(out)
	runMain(outPtr.Main)
	restoreSpriteUserFields(userState)
	return dest
}

func instantiateStageSprite(out reflect.Value, source Sprite, properties spriteProperties) (*SpriteImpl, Sprite) {
	// 配置中的静态舞台实例不走运行时克隆发布门控，也不执行 Clone 生命周期。
	dest, outPtr := copySprite(out, source)
	applySpriteProperties(dest, properties)
	dest.initRuntimeProxy()
	return dest, outPtr
}

func copySprite(out reflect.Value, source Sprite) (*SpriteImpl, Sprite) {
	// out.Set 先复制生成结构体，再让每个含 InitFrom 的嵌入字段重建克隆私有状态。
	in := reflect.ValueOf(source).Elem()
	outPtr := out.Addr().Interface().(Sprite)
	dest := spriteOf(outPtr)
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
	dest.sprite = outPtr
	dest.runtimeState.IsCostumeDirty = true
	// 新对象使用独立代理，即使图层数值未变也必须重新提交。
	dest.runtimeState.IsLayerDirty = true

	src := spriteOf(source)
	dest.components = cloneComponents(src, dest)
	return dest, outPtr
}

type spriteUserFieldSnapshot struct {
	field reflect.Value
	value reflect.Value
}

func snapshotSpriteUserFields(v reflect.Value) []spriteUserFieldSnapshot {
	// SpriteImpl 由 InitFrom 管理；其余生成字段视为项目用户状态。
	count := v.NumField()
	out := make([]spriteUserFieldSnapshot, 0, count)
	typ := v.Type()
	for i := 0; i < count; i++ {
		if isSpriteBaseField(typ, i) {
			continue
		}
		field := settableSpriteField(v.Field(i))
		if !field.IsValid() {
			continue
		}
		saved := reflect.New(field.Type()).Elem()
		saved.Set(field)
		out = append(out, spriteUserFieldSnapshot{field: field, value: saved})
	}
	return out
}

func restoreSpriteUserFields(state []spriteUserFieldSnapshot) {
	for _, saved := range state {
		saved.field.Set(saved.value)
	}
}

func settableSpriteField(field reflect.Value) reflect.Value {
	if field.CanSet() {
		return field
	}
	if !field.CanAddr() {
		return reflect.Value{}
	}
	// 生成结构可能含未导出字段；使用原地址建立同类型可设置视图。
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
}

// dispatchCloneLifecycle 立即派发新精灵自己的 OnCloned，并在首段执行后开放代理发布。
//
// 直接调用方：doClone；总体流程调用方：精灵脚本的 Clone API。它不写入 Game.events，
// 也不经过 pending/ready 或等待下一次 Engine.Update；doWhenCloned 会在当前调用链中
// 创建处理器协程，并以 BatchWaitFirstSlice 等到所有命中处理器首次挂起或结束。
// defer 保证没有注册 OnCloned，或处理器首段正常返回时，也能把代理推进到 Ready。
func dispatchCloneLifecycle(dest *SpriteImpl, data any) {
	// doWhenCloned 返回的时点是“首段已运行”，defer 随后将代理推进到 Ready。
	defer dest.finishCloneInitialization()
	if dest.spriteState.HasOnCloned {
		dest.doWhenCloned(dest, data)
	}
}
