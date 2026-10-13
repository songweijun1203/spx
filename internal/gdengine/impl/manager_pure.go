//go:build pure_engine

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

// pure_engine Manager 不连接 Godot，直接嵌入 enginewrap/sync_pure.gen.go 的方法桩。
package impl

import (
	"fmt"
	"reflect"

	"github.com/goplus/spx/v3/internal/enginewrap"
	. "github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

type audioMgr struct {
	baseMgr
	enginewrap.AudioMgrImpl
}

type cameraMgr struct {
	baseMgr
	enginewrap.CameraMgrImpl
}

type debugMgr struct {
	baseMgr
	enginewrap.DebugMgrImpl
}

type extMgr struct {
	baseMgr
	enginewrap.ExtMgrImpl
}

type inputMgr struct {
	baseMgr
	enginewrap.InputMgrImpl
}

type navigationMgr struct {
	baseMgr
	enginewrap.NavigationMgrImpl
}

type penMgr struct {
	baseMgr
	enginewrap.PenMgrImpl
}

type physicsMgr struct {
	baseMgr
	enginewrap.PhysicsMgrImpl
}

type platformMgr struct {
	baseMgr
	enginewrap.PlatformMgrImpl
}

type resMgr struct {
	baseMgr
	enginewrap.ResMgrImpl
}

type sceneMgr struct {
	baseMgr
	enginewrap.SceneMgrImpl
}

type spriteMgr struct {
	baseMgr
	enginewrap.SpriteMgrImpl
}

type tilemapMgr struct {
	baseMgr
	enginewrap.TilemapMgrImpl
}

type tilemapparserMgr struct {
	baseMgr
	enginewrap.TilemapparserMgrImpl
}

type uiMgr struct {
	baseMgr
	enginewrap.UiMgrImpl
}

func (*platformMgr) IsMainThread() bool { return true }

// BindMgr 把 pure_engine Manager 赋给 engine 包的全局接口变量。
// 直接上级：gdengine.PrepareLink()。
func BindMgr(mgrs []IManager) {
	for _, mgr := range mgrs {
		switch v := mgr.(type) {
		case IAudioMgr:
			AudioMgr = v
		case ICameraMgr:
			CameraMgr = v
		case IDebugMgr:
			DebugMgr = v
		case IExtMgr:
			ExtMgr = v
		case IInputMgr:
			InputMgr = v
		case INavigationMgr:
			NavigationMgr = v
		case IPenMgr:
			PenMgr = v
		case IPhysicsMgr:
			PhysicsMgr = v
		case IPlatformMgr:
			PlatformMgr = v
		case IResMgr:
			ResMgr = v
		case ISceneMgr:
			SceneMgr = v
		case ISpriteMgr:
			SpriteMgr = v
		case ITilemapMgr:
			TilemapMgr = v
		case ITilemapparserMgr:
			TilemapparserMgr = v
		case IUiMgr:
			UiMgr = v
		default:
			panic(fmt.Sprintf("engine init error : unknown manager type %s", reflect.TypeOf(mgr).String()))
		}
	}
}

// createMgrs 创建 pure_engine 使用的全部 Manager。
// 直接上级：manager_base.go 的 CreateMgrs()。
func createMgrs() []IManager {
	addManager(&audioMgr{})
	addManager(&cameraMgr{})
	addManager(&debugMgr{})
	addManager(&extMgr{})
	addManager(&inputMgr{})
	addManager(&navigationMgr{})
	addManager(&penMgr{})
	addManager(&physicsMgr{})
	addManager(&platformMgr{})
	addManager(&resMgr{})
	addManager(&sceneMgr{})
	addManager(&spriteMgr{})
	addManager(&tilemapMgr{})
	addManager(&tilemapparserMgr{})
	addManager(&uiMgr{})
	return mgrs
}
