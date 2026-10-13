//go:build !js && !pure_engine

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

package facade

import (
	"github.com/goplus/spx/v3/internal/gdengine/binding/native"
	"github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

// LinkFFI 建立 Native 平台的运行期绑定会话。
//
// 平台：仅 Native（PC/桌面及其他非 js、非 pure_engine 目标）编译。
// 调用时机：internal/engine.Main() 执行 gdengine.PrepareLink() 时。
// 直接上级：internal/gdengine.PrepareLink()。
// 跨模块来源：Native 的底层函数表已由 Godot -> gdspx_init() 提前完成解析，
// 因此这里不再重复初始化，只返回一个立即就绪的会话。
// 返回值：第一个值是本次绑定的生命周期句柄；第二个值表示 Web 解释器模式，Native 固定为 false。
func LinkFFI() (*LinkSession, bool) {
	return newLinkSession(func(ready func()) {
		ready()
	}, nil), false
}

// RegisterCallbacks 把公共层生成的 Go 回调表保存到 Native FFI 层。
// 直接上级：internal/gdengine.PrepareLink()。
// 后续来源：Godot C++ -> func_on_xxx() -> ffi.callbacks -> internal/gdengine 回调。
func RegisterCallbacks(callbacks engine.CallbackInfo) {
	ffi.BindCallback(callbacks)
}
