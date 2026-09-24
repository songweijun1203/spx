//go:build js && !pure_engine

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
	"github.com/goplus/spx/v3/internal/gdengine/binding/web"
	"github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

// LinkFFI 建立 Web 平台的 JavaScript FFI 会话。
//
// 平台：仅 Web（GOOS=js 且非 pure_engine）编译。
// 调用时机：internal/engine.Main() 执行 gdengine.PrepareLink() 时。
// 直接上级：internal/gdengine.PrepareLink()。
// 跨模块来源：它继续调用 webffi.Link()，注册 Go 回调入口并解析 globalThis 上的
// gdspx_* JavaScript 函数，建立 Go WASM 与 Godot WASM 之间的双向通道。
// 返回值：第一个值统一管理本次 Web 绑定的 Run/Unlink；第二个值表示是否为解释器模式。
func LinkFFI() (*LinkSession, bool) {
	link, interpreter := webffi.Link()
	return newLinkSession(link.Run, link.Unlink), interpreter
}

// RegisterCallbacks 把公共层生成的 Go 回调表保存到 Web FFI 层。
// 直接上级：internal/gdengine.PrepareLink()。
// 后续来源：Godot -> library_godot_gdspx.js -> gdspx_dispatch -> 该回调表。
func RegisterCallbacks(callbacks engine.CallbackInfo) {
	webffi.BindCallback(callbacks)
}
