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

package impl

//lint:file-ignore ST1001 Godot manager glue intentionally dot-imports engine API types.

import (
	. "github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

var (
	// mgrs 保存当前 createMgrs() 创建的全部平台 Manager。
	mgrs []IManager
)

// baseMgr 为不关心某些生命周期事件的具体 Manager 提供空实现。
// 平台：Native、Web 和 pure_engine 共用。
type baseMgr struct{}

func (pself *baseMgr) OnStart() {}

func (pself *baseMgr) OnUpdate(delta float64) {}

func (pself *baseMgr) OnFixedUpdate(delta float64) {}

func (pself *baseMgr) OnDestroy() {}

func (pself *baseMgr) OnPause(isPaused bool) {}

// CreateMgrs 清空旧列表并调用编译期选中的 createMgrs() 创建所有 Manager。
//
// 平台：公共入口；Native/Web/pure_engine 各自提供 createMgrs() 实现。
// 直接上级：gdengine.PrepareLink()。
// 调用时机：平台 FFI 与回调表完成绑定之后、全局 Manager 接口赋值之前。
func CreateMgrs() []IManager {
	// 确保重复创建会话时，不会残留上一次的 Manager。
	mgrs = mgrs[:0]
	return createMgrs()
}

// addManager 按创建顺序保存一个 Manager，并返回其具体类型值。
// 直接上级：各平台生成的 createMgrs()。
func addManager[T IManager](mgr T) T {
	mgrs = append(mgrs, mgr)
	return mgr
}
