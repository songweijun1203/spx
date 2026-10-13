//go:build js

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

package webffi

import (
	"syscall/js"

	spxlog "github.com/goplus/spx/v3/internal/log"
	"github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

// LinkSession 保存一次 Web FFI 绑定特有的运行状态。
// 它由 Link() 创建，经 facade.LinkSession 间接交给 gdengine 持有。
type LinkSession struct {
	// exit 用于保持 Web 会话存活，Unlink() 关闭它后 Run() 才会返回。
	exit chan struct{}
	// interpreter 表示当前没有等待真实 Godot OnEngineStart，需要由 Go 主动补发启动事件。
	interpreter bool
}

var (
	// callbacks 保存公共 gdengine 层生成的 Go 回调表，由 gdspxDispatch() 按事件名调用。
	callbacks engine.CallbackInfo
	// hasInitEngine 记录外部 Web 引擎是否完成初始化握手。
	hasInitEngine bool
	// js.Func 必须长期持有，否则 Go GC 后 globalThis 上的 JavaScript 函数将失效。
	goWasmInitCallbackHandle js.Func
	callbackDispatcherHandle js.Func
)

// Link 建立 Web 平台的双向绑定。
//
// 平台：仅 Web（GOOS=js）。
// 调用时机：Go 游戏进入 internal/engine.Main() 并执行 gdengine.PrepareLink() 时。
// 直接上级：binding/facade 的 Web 版 LinkFFI()。
// 跨模块来源：internal/engine.Main() -> gdengine.PrepareLink() -> facade.LinkFFI()。
//
// 本函数先把 Go 回调入口注册到 globalThis，再取得 JavaScript 已提供的 gdspx_* 调用入口，
// 从而同时接通 Godot -> Go 回调方向和 Go -> Godot Manager 调用方向。
func Link() (*LinkSession, bool) {
	registerWebGlobals()
	// 通过 globalThis 查找 gdspx_* JavaScript 函数，并保存到 API 结构体。
	// 这些函数最终由 js/engine/gdspx.js 调用 Module['_gdspx_*']。
	API.resolveAPIFunctions()
	interpreter := !hasInitEngine
	// session 用于控制本次 Web 绑定何时结束；interpreter 作为独立返回值交给公共层，
	// 用于记录运行模式。它不是错误标记。
	return &LinkSession{exit: make(chan struct{}), interpreter: interpreter}, interpreter
}

// Run 进入 Web 绑定会话并等待 Unlink()。
//
// 直接上级：facade.LinkSession.Run()，再上一级是 internal/engine.Main()。
// 解释器模式没有 Godot C++ 发出的真实 OnEngineStart，因此在 ready 前主动补发一次。
func (s *LinkSession) Run(ready func()) {
	// 解释器模式下没有真正的 Godot C++ OnEngineStart 回调，
	// 因此手动派发一次同名事件；正常 Worker 模式由 Godot JS 回调触发。
	if s.interpreter {
		gdspxDispatch(js.Value{}, []js.Value{jsEventOnEngineStart})
	}
	// 通知 internal/engine.Main：Manager 和回调已经准备好。
	ready()
	// 保持 Web FFI 会话存活，直到 Unlink() 关闭 exit。
	<-s.exit
}

// Unlink 结束 Web 绑定会话并复位初始化握手状态。
// 直接上级：facade.LinkSession.Unlink()，通常由游戏退出、销毁或重置流程调用。
func (s *LinkSession) Unlink() {
	// 结束 LinkSession；Run() 中阻塞的 goroutine 会从 exit 返回。
	close(s.exit)
	hasInitEngine = false
}

// BindCallback 保存 Web 平台的 Go 回调表。
//
// 直接上级：binding/facade 的 Web 版 RegisterCallbacks()。
// 跨模块来源：gdengine.PrepareLink() -> facade.RegisterCallbacks() -> 本函数。
// 后续由 library_godot_gdspx.js -> FFI.gdspx_dispatch -> gdspxDispatch() 使用。
func BindCallback(info engine.CallbackInfo) {
	// 保存 internal/gdengine 生成的 Go 回调表。
	// gdspxDispatch() 收到事件后，会根据事件名调用这里的函数。
	contactEventGeneration++
	callbacks = info
}

// resolveJSFunc 从当前 JavaScript 全局对象中取得指定的 gdspx_* 函数。
//
// 平台：仅 Web。
// 直接上级：生成代码 API.resolveAPIFunctions()。
// 跨模块目标：js/engine/gdspx.js 中 GdspxFuncs 注册到 globalThis 的方法。
func resolveJSFunc(funcName string) js.Value {
	// Web 侧不使用 Native 的 lookupFunc；直接从当前 Worker 的 globalThis
	// 查找 game.js/go.wasm.loader.js 注册的 gdspx_* 方法。
	val := js.Global().Get(funcName)
	if val.IsUndefined() || val.IsNull() {
		panic("JS function not found: " + funcName)
	}
	return val
}

// registerWebGlobals 把 JavaScript 可调用的 Go 入口注册到当前 globalThis。
//
// 平台：仅 Web。
// 直接上级：Link()。
// 注册结果主要服务于 Godot JavaScript -> Go 回调，与 resolveAPIFunctions() 的方向相反。
func registerWebGlobals() {
	// 注册供 Worker loader 调用的 Go 初始化握手函数。
	if goWasmInitCallbackHandle.Type() == js.TypeUndefined {
		goWasmInitCallbackHandle = js.FuncOf(goWasmInit)
		js.Global().Set("go_wasm_init", goWasmInitCallbackHandle)
	}
	registerCallbackDispatcher()
	registerContactEventQueue()
}

// registerCallbackDispatcher 注册统一的 Godot JavaScript -> Go 事件分发入口。
// 直接上级：registerWebGlobals()；JavaScript 侧最终通过 FFI.gdspx_dispatch 调用它。
func registerCallbackDispatcher() {
	// 注册 Godot JS → Go 的唯一事件分发入口。
	if callbackDispatcherHandle.Type() == js.TypeUndefined {
		callbackDispatcherHandle = js.FuncOf(gdspxDispatch)
		js.Global().Set("gdspx_dispatch", callbackDispatcherHandle)
	}
}

// goWasmInit 是 Web 引擎与 Go WASM 的初始化握手入口。
//
// 平台：仅 Web，主要供 Worker 加载流程使用，普通 Native/PC 不会调用。
// 直接上级：JavaScript go.wasm.loader.js -> goBridge.callGoFunctionSafe("go_wasm_init")。
// 它只更新状态，不负责解析 API；真正的 API 解析由之后的 Link() 完成。
func goWasmInit(this js.Value, args []js.Value) any {
	spxlog.Info("WASM initialized")
	hasInitEngine = true
	return js.ValueOf(nil)
}
