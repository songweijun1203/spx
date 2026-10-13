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

//lint:file-ignore ST1001 Godot linker glue intentionally dot-imports engine API types.

import (
	"sync"

	"github.com/goplus/spx/v3/internal/gdengine/binding/facade"
	engineimpl "github.com/goplus/spx/v3/internal/gdengine/impl"
	. "github.com/goplus/spx/v3/pkg/spx/pkg/engine"
	gdspx "github.com/goplus/spx/v3/pkg/spx/pkg/gdspx"
)

var (
	// mgrs 保存本次游戏会话创建的全部 Manager，供统一分发生命周期事件。
	mgrs []IManager
	// coreCallbacks 由 internal/engine.Main() 提供，负责把 Godot 事件继续交给游戏运行时。
	coreCallbacks CoreCallbackInfo
	sprites       = make([]ISpriter, 0)
	// isWebIntepreterMode 仅在 Web 解释器模式下为 true；Native 平台始终为 false。
	isWebIntepreterMode bool
	// activeLink 表示当前唯一的 Godot 绑定会话，游戏退出或重置时由 Unlink 清理。
	activeLink *LinkSession
	linkMu     sync.Mutex
)

// LinkSession 是 gdengine 暴露给 internal/engine 的会话句柄。
//
// 它代表“本次游戏已经安装的 FFI、回调表和 Manager”及其清理权，游戏启动后保存于
// internal/engine.gameBinding.link，退出或重置时通过同一个对象执行 Unlink()。
// 平台：公共层，Native 与 Web 都会使用；backend 的具体实现由 facade 的编译标签决定。
type LinkSession struct {
	// backend 负责启动一次、清理一次等公共并发约束，并包装具体平台的 run/unlink。
	backend *facade.LinkSession
}

// linkerBridge 把公开包 pkg/spx/pkg/gdspx 的兼容接口转发到 internal/gdengine。
// 平台：公共层，Native、Web 和 pure_engine 都会编译。
type linkerBridge struct{}

// init 在 Go 包初始化阶段注册 linkerBridge。
//
// 直接上级：Go 运行时加载 internal/gdengine 包，没有普通 Go 函数调用者。
// 跨模块来源：pkg/spx/pkg/gdspx 只依赖 LinkerBridge 接口，借此避免反向导入 internal 包。
func init() {
	gdspx.SetLinkerBridge(linkerBridge{})
}

// IsWebIntepreterMode 实现 gdspx.LinkerBridge，仅返回当前 Web 解释器模式标记。
// 直接上级：pkg/spx/pkg/gdspx.IsWebIntepreterMode()。
func (linkerBridge) IsWebIntepreterMode() bool {
	return IsWebIntepreterMode()
}

// Link 实现 gdspx.LinkerBridge 的兼容入口。
// 直接上级：pkg/spx/pkg/gdspx.LinkEngine()；新启动链主要直接调用 PrepareLink()。
func (linkerBridge) Link(coreCallbackInfo CoreCallbackInfo) {
	Link(coreCallbackInfo)
}

// Unlink 实现 gdspx.LinkerBridge 的兼容清理入口。
// 直接上级：pkg/spx/pkg/gdspx.UnlinkEngine()。
func (linkerBridge) Unlink() {
	Unlink()
}

// IsWebIntepreterMode 返回当前是否为 Web 解释器运行方式。
// 平台：公共查询接口；Native 返回 false，Web 的值由 webffi.Link() 判定。
func IsWebIntepreterMode() bool {
	return isWebIntepreterMode
}

// Link 是旧式同步绑定入口，建立连接后立即进入平台会话。
// 直接上级：linkerBridge.Link()；内部调用 PrepareLink().Run()。
func Link(coreCallbackInfo CoreCallbackInfo) {
	PrepareLink(coreCallbackInfo).Run(nil)
}

// PrepareLink 在进入平台运行阶段前，依次安装 FFI、回调表和全部 Manager。
//
// 平台：公共层；facade.LinkFFI() 会在编译期选择 Native、Web 或 pure_engine 实现。
// 调用时机：游戏对象已绑定、internal/engine.Main() 即将启动后端会话时。
// 直接上级：internal/engine.Main()。
// 跨模块来源：游戏入口 -> internal/engine.Main() -> gdengine.PrepareLink()。
func PrepareLink(coreCallbackInfo CoreCallbackInfo) *LinkSession {
	linkMu.Lock()
	defer linkMu.Unlock()
	if activeLink != nil {
		panic("gdengine: a link is already active")
	}

	// LinkFFI 返回本次绑定的平台生命周期句柄；第二个返回值只表示 Web 解释器模式，
	// 不代表绑定成功与否。函数失败时会直接 panic，而不是返回 false。
	backend, interpreter := facade.LinkFFI()
	session := &LinkSession{backend: backend}
	// LinkFFI 已经创建平台资源，但只有回调和 Manager 全部安装后才能提交为 activeLink。
	// 中途发生 panic 时，defer 会通过刚返回的 backend 回滚本次绑定。
	committed := false
	defer func() {
		if !committed {
			backend.Unlink()
			mgrs = nil
			coreCallbacks = CoreCallbackInfo{}
			isWebIntepreterMode = false
		}
	}()

	isWebIntepreterMode = interpreter
	coreCallbacks = coreCallbackInfo
	// 顺序不能颠倒：先让平台回调能找到公共回调表，再创建并绑定游戏可见的 Manager。
	facade.RegisterCallbacks(bindCallbacks())
	mgrs = engineimpl.CreateMgrs()
	engineimpl.BindMgr(mgrs)
	activeLink = session
	committed = true
	return session
}

// Run 启动当前平台的绑定会话，并在平台准备完成时调用 ready。
// 直接上级：internal/engine.Main()；再转发给 facade.LinkSession.Run()。
// ready 当前用于关闭 gameBinding.startDone，通知退出/重置流程“启动状态已稳定”。
func (s *LinkSession) Run(ready func()) {
	s.backend.Run(ready)
}

// Unlink 释放当前会话的平台资源，并清空 Manager、回调和模式状态。
// 直接上级：internal/engine.(*gameBinding).unlink()，发生在游戏退出、销毁或重置收尾阶段。
func (s *LinkSession) Unlink() {
	if s == nil {
		return
	}
	s.backend.Unlink()
	linkMu.Lock()
	defer linkMu.Unlock()
	if activeLink != s {
		return
	}
	activeLink = nil
	mgrs = nil
	coreCallbacks = CoreCallbackInfo{}
	isWebIntepreterMode = false
}

// Unlink 是兼容用的全局清理入口，关闭当前活动会话。
// 直接上级：linkerBridge.Unlink() -> pkg/spx/pkg/gdspx.UnlinkEngine()。
func Unlink() {
	linkMu.Lock()
	session := activeLink
	linkMu.Unlock()
	session.Unlink()
}
