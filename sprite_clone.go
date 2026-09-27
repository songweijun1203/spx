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

var spriteImplType = reflect.TypeFor[SpriteImpl]()

// 本文件负责“运行时克隆”，完整调用链如下：
//
//	Clone__0/Clone__1 -> CloneWith -> doClone -> createRuntimeClone
//	  -> cloneSprite（复制 Go 对象、创建隐藏的 Godot 代理、awake、重跑 Main）
//	  -> Game.addClonedShape -> shapeManager.addClonedShape
//	  -> dispatchCloneLifecycle -> doWhenCloned -> dispatchTarget
//	  -> gco.StartBatch(BatchWaitFirstSlice)
//	  -> finishCloneInitialization（Pending -> Ready）
//	  -> OnEngineRender/OnEngineUpdate 的代理批处理（Ready -> Published）

// Clone__0 是不携带用户数据的脚本克隆入口。
//
// 直接调用方：项目生成代码中的 this.Clone__0()；总体流程调用方：精灵脚本事件协程。
func (p *SpriteImpl) Clone__0() {
	p.CloneWith(nil)
}

// Clone__1 是携带任意用户数据的脚本克隆入口，数据最终传给新精灵的 OnCloned。
//
// 直接调用方：项目生成代码中的 this.Clone__1(data)；总体流程调用方：精灵脚本事件协程。
func (p *SpriteImpl) Clone__1(data any) {
	p.CloneWith(data)
}

// CloneWith 把公开 API 收敛到统一的运行时克隆流程。
//
// 直接调用方：Clone__0、Clone__1；总体流程调用方：精灵脚本发起的 Clone 操作。
func (p *SpriteImpl) CloneWith(__xgo_optional_data any) {
	doClone(p.sprite, __xgo_optional_data, nil)
}

// TODO(xsw): use classfile clone mechanism instead of reflection.
// doClone 编排一次运行时克隆，并在 Go 对象已加入 shapeManager 后派发 OnCloned。
//
// 直接调用方：CloneWith，以及需要观察新对象的内部测试；总体流程调用方：精灵脚本的
// Clone API。onCloned 是内部观察钩子，不是用户的 OnCloned 事件；生产入口传 nil。
// 本函数返回前只保证 OnCloned 已经执行到第一处挂起或结束，并不等待处理器全部完成；
// Godot 代理的正式公开也由后续帧边界批处理完成。
func doClone(sprite Sprite, data any, onCloned func(sprite *SpriteImpl)) {
	if sprite == nil {
		spxlog.Panicf("DoClone: sprite is nil")
	}
	// 第一阶段完成 Go 对象、隐藏 Godot 代理和 shapeManager 登记。
	src := spriteOf(sprite)
	dest := createRuntimeClone(src)
	if dest == nil {
		return
	}
	// 新对象继承了源对象的可见状态；可见时请求舞台在本帧重新绘制。
	dest.requestRedrawIfVisible()
	if onCloned != nil {
		onCloned(dest)
	}
	// 第二阶段在当前调用链派发克隆专属生命周期，并建立首段执行屏障。
	dispatchCloneLifecycle(dest, data)
}

// createRuntimeClone 创建克隆的 Go 对象和独立 Godot 代理，并把它插入活动 Shape 列表。
//
// 直接调用方：doClone；总体流程调用方：精灵脚本的 Clone API。reserveClone 在真正
// 插入列表前预占名额，防止 Main/OnCloned 挂起或嵌套克隆时绕过 300 个克隆的上限。
// 这里按源对象的动态类型反射分配，因此生成精灵结构体中的用户字段也会被复制。
func createRuntimeClone(src *SpriteImpl) *SpriteImpl {
	shapes := &src.g.shapeMgr
	// 先预占名额，因为下面的 Main 可以挂起或递归创建更多克隆。
	if !shapes.reserveClone() {
		return nil
	}
	defer func() { shapes.pendingClones-- }()

	if isDebugInstrEnabled() {
		spxlog.Debug("Clone: %s", src.name)
	}
	// 按源精灵的实际生成类型分配新对象，而不是只分配一个 SpriteImpl。
	in := reflect.ValueOf(src.sprite).Elem()
	v := reflect.New(in.Type())
	out, outPtr := v.Elem(), v.Interface().(Sprite)
	// cloneSprite 返回时，克隆自己的 Main 已完成，事件也已经重新注册。
	dest := cloneSprite(out, outPtr, in, nil)
	// 必须先加入活动列表再派发 OnCloned，使处理器能够查询、排序或删除自己。
	src.g.addClonedShape(src, dest)
	return dest
}

// cloneSprite 复制并初始化一个精灵；v == nil 表示运行时克隆，v != nil 表示项目加载。
//
// 直接调用方：createRuntimeClone、applySprite；总体流程调用方分别是运行时 Clone 和
// 项目配置加载。运行时克隆按以下顺序执行：
//  1. out.Set 复制生成精灵结构体，再对内嵌字段调用 InitFrom，重建对象私有状态；
//  2. 复制组件，并把代理公开状态设为 Pending；
//  3. initRuntimeProxy 在 Godot 主线程创建独立且隐藏的 SpxSprite 节点；
//  4. awake 设置唤醒状态；
//  5. 在当前调用栈重跑 Main，使 OnClick/OnMsg/OnCloned 等以 dest 为 owner 重新注册。
//
// Main 不是在这里作为新协程启动的；若 Clone 来自事件协程，Main 就占用该协程当前
// 执行片段。重跑 Main 可能再次执行 XGo_Init，所以先快照用户字段，结束后恢复字段值；
// 事件注册属于共享 registry 中的外部副作用，不会被恢复，正是克隆所需的结果。
func cloneSprite(out reflect.Value, outPtr Sprite, in reflect.Value, v coreproject.StageShape) *SpriteImpl {
	dest := spriteOf(outPtr)
	func() {
		// 先浅复制整个生成结构体；随后各内嵌运行时字段用 InitFrom 重建克隆私有状态。
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

	// 组件不能直接沿用源对象中的 owner，要重新绑定到 dest。
	src := spriteOf(in.Addr().Interface().(Sprite))
	dest.components.cloneFrom(src, dest)

	if v != nil {
		// 项目加载路径使用配置覆盖模板属性，awake/Main 由 bootstrap 稍后统一执行。
		applySpriteProps(dest, v)
	} else {
		// 运行时克隆先开启公开门控，确保新建的 Godot 节点从第一刻起保持隐藏。
		dest.beginCloneProxyPublication()
	}
	// 创建克隆自己的 Godot SpxSprite；内部会清除浅复制带来的源 SyncSprite 引用。
	dest.initRuntimeProxy()
	if v == nil {
		// 运行时克隆先 awake，再重跑 Main；因此 Main 中读取 IsAwakened 会得到 true。
		dest.awake()
		// 重跑 Main 会重新注册克隆事件，也会再次执行 XGo_Init；先保存顶层用户字段，
		// Main 返回后恢复字段值，同时保留事件 registry 中新产生的注册副作用。
		userState := snapshotSpriteUserFields(out)
		runMain(outPtr.Main)
		restoreSpriteUserFields(out, userState)
	}
	return dest
}

// snapshotSpriteUserFields 保存生成精灵结构体中除 SpriteImpl 外的顶层用户字段。
//
// 直接调用方：cloneSprite；总体流程调用方：运行时克隆重跑 Main 前的状态保护。
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

// restoreSpriteUserFields 恢复重跑 Main 前保存的用户字段。
//
// 直接调用方：cloneSprite；总体流程调用方：运行时克隆重跑 Main 后的状态保护。
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
