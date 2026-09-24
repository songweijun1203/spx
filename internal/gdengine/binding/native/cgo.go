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

package ffi

import (
	"unsafe"

	"github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)
import "C"

var (
	// resolveCFunc 封装 Godot 传入的 lookupFunc，用名称查询 Native 函数地址。
	// 它在 gdspx_init() 中赋值，并在整个动态库生命周期内供函数表解析使用。
	resolveCFunc func(string) unsafe.Pointer
	// callbacks 保存 internal/gdengine 组装的 Go 回调表。
	// Godot C++ 调用 func_on_xxx 后，会从该表继续转发到公共引擎层。
	callbacks engine.CallbackInfo
)

// BindCallback 保存 Native 平台的 Go 回调表。
//
// 平台：仅 Native（PC/桌面等非 Web 目标）。
// 调用时机：Go 游戏执行 gdengine.PrepareLink() 期间。
// 直接上级：binding/facade.RegisterCallbacks()。
// 跨模块来源：internal/engine.Main() -> gdengine.PrepareLink() -> facade.RegisterCallbacks()。
func BindCallback(info engine.CallbackInfo) {
	callbacks = info
}

// main 链接到最终可执行模块中的 main.main。
// 直接上级：Godot 在 Scene 初始化级别调用 initialize() 后，由 initialize() 启动。
//
//go:linkname main main.main
func main()

// gdspx_init 是 Native 平台的 GDExtension 动态库入口。
//
// 平台：仅 Native；Web 不加载该入口。
// 调用时机：Godot 根据 gdspx.gdextension 加载动态库时，早于 Go 游戏的 engine.Main()。
// 直接上级：Godot GDExtension 加载器，不存在普通 Go 上级函数。
// 跨模块来源：Godot -> 动态库 entry_symbol="gdspx_init" -> 本函数。
//
// 此处完成的是“扩展装载期”初始化：保存符号查询器、解析基础/Manager 函数表、
// 填写 Godot 初始化结构，并把 Go 的 func_on_xxx 回调地址注册到 godot_modules。
//
//export gdspx_init
func gdspx_init(lookupFunc uintptr, classes, configuration unsafe.Pointer) uint8 {
	_ = classes // 预留给后续的类注册功能。
	// 第一步：保存 Godot 提供的函数查询入口，后续按函数名获取真实 C/C++ 地址。
	resolveCFunc = func(s string) unsafe.Pointer {
		return getProcAddress(lookupFunc, s)
	}

	// 第二步：解析 Godot 基础 API。doInitialization() 会立即使用其中的
	// Variant/String 构造能力，因此 builtinAPI 不能直接延迟到 facade.LinkFFI()。
	builtinAPI.resolveAPIFunctions()
	// 第三步：解析 spx_audio_xxx、spx_sprite_xxx、spx_pen_xxx 等 Manager API。
	// 这些接口主要在 PrepareLink() 创建 Manager 后使用；当前选择在扩展加载期提前校验。
	api.resolveAPIFunctions()
	if api.SpxPlatformIsMainThread == nil {
		panic("gdengine: spx_platform_is_main_thread is unavailable")
	}
	// 第四步：告诉 Godot 本扩展从 Scene 级别开始初始化，并提供 initialize/deinitialize。
	init := (*initialization)(configuration)
	*init = initialization{}
	init.minimum_initialization_level = initializationLevel(GDExtensionInitializationLevelScene)
	doInitialization(init)
	// 第五步：把 Native 的 func_on_xxx 函数表交给 godot_modules，建立 Godot -> Go 回调。
	registerEngineCallback()
	return 1
}
