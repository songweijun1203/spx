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
	"flag"
	"fmt"
	"os"
	"reflect"

	spxfs "github.com/goplus/spx/v3/fs"
	coreproject "github.com/goplus/spx/v3/internal/core/project"
	"github.com/goplus/spx/v3/internal/engine"
)

// loadGame 在 Game.OnEngineStart() 的启动任务中加载项目运行时数据。
//
// 它读取已经写入虚拟文件系统的 project/assets 数据，应用项目配置，
// 建立舞台和精灵的 Go 状态，并通过 Sprite Manager 创建必要的 Godot
// 运行代理。精灵 awake、精灵 Main 和 OnLoaded 不在这里直接执行，
// 而是由 loadStage() 登记到 bootstrap 队列，交给 startBootstrap() 执行。
//
// resource 通常是 "assets"；generation 用于让异步加载结果与当前一局匹配。
func (p *Game) loadGame(resource any, generation uint64) error {
	// 从 Godot 虚拟文件系统打开项目配置、字体信息和项目文件系统视图。
	opened, err := coreproject.OpenBuilderResources(resource, nil)
	if err != nil {
		return err
	}
	// 告诉 Go/引擎后续资源路径从哪个资产目录解析。
	if opened.AssetDir != "" {
		engine.SetAssetDir(opened.AssetDir)
	}
	// 先解析项目字体计划，再交给 ResMgr 应用默认字体和字体缓存配置。
	fontPlan := coreproject.ResolveRuntimeFontPlan(opened.Fonts, engine.ToAssetPath)
	if err := applyRuntimeFontPlan(&engine.Managers().ResMgr, fontPlan); err != nil {
		return fmt.Errorf("apply project fonts: %w", err)
	}

	conf, proj := &opened.Config, &opened.Project
	// 解析项目启动参数、显示/物理/音频等运行配置。
	parseCommandLineFlags(conf)
	p.applyRuntimeConfig(conf, proj)
	setupGameSystems(p, proj)
	// 根据 Go 类型表和精灵配置创建 Go 精灵对象；其运行代理会通过
	// SpriteMgr 映射到 Godot 节点。
	gamer := reflect.ValueOf(p.gamer).Elem()
	loadGameSprites(p, gamer, opened.FS, proj)
	// 加载舞台、背景、相机、Tilemap 和 Z 序，并登记 bootstrap 回调。
	p.loadStage(gamer, proj, generation, p.loadSprite)

	platform := &engine.Managers().PlatformMgr
	// 设置窗口焦点行为、事件循环和窗口标题等平台状态。
	if !conf.DontRunOnUnfocused {
		platform.SetRunnableOnUnfocused(true)
	}
	p.initEventLoop()
	platform.SetWindowTitle(p.runtimeConfigInput.Title)
	return nil
}

// startLoad 初始化项目资源加载阶段使用的 Go 子系统：声音、输入、事件队列
// 和项目文件系统视图。它为后续 loadSprite/loadStage 提供运行时上下文。
func (p *Game) startLoad(fs spxfs.Dir) {
	p.soundMgr.Init(&engine.Managers().AudioMgr)
	p.sounds = make(map[string]sound)
	p.inputMgr.init(p)
	p.events = make(chan event, eventBufferSize)
	p.eventQueueState.EventQueueStats.Reset()
	p.fs = fs
}

// -----------------------------------------------------------------------------
// Setup
// -----------------------------------------------------------------------------
// setupGameSystems 应用项目级物理、音频、路径查找和渲染层排序配置。
// 这里配置的是运行系统，不执行精灵生命周期脚本。
func setupGameSystems(g *Game, proj *coreproject.ProjectConfig) {
	settings := coreproject.ResolveSystemSettings(proj)
	if settings.AutoSetCollisionLayer == g.physicsEnabled {
		engine.Panic("invalid configuration: autoSetCollisionLayer and physics enabled state must not be the same")
	}
	engine.SetLayerSortMode(settings.LayerSortMode)
	g.applyPathFinderSettings(settings)
	g.applyAudioSettings(settings)
	g.applyPhysicsSettings(settings)
}

// -----------------------------------------------------------------------------
// Loading
// -----------------------------------------------------------------------------
// loadGameSprites 遍历 Gamer 的 Go 字段，加载字段对应的精灵配置，
// 并初始化项目精灵原型和 Tilemap 数据。
func loadGameSprites(g *Game, v reflect.Value, fs spxfs.Dir, proj *coreproject.ProjectConfig) {
	g.startLoad(fs)
	//WalkFields ？需要再次理解 todo
	err := coreproject.WalkFields(v, func(fieldIndex int) (string, any) {
		return getFieldPtrOrAlloc(g, v, fieldIndex)
	}, func(name string, val any) error {
		fld, ok := val.(Sprite)
		if !ok || g.typs[name] == nil {
			return nil
		}
		return g.loadSprite(fld, name, v)
	})
	if err != nil {
		engine.Panic(err)
	}
	g.tilemapMgr.init(g, fs, proj.TilemapPath)
}

func parseCommandLineFlags(conf *Config) {
	f := flag.CommandLine
	effects, err := coreproject.ParseCommandLineFlags(f, os.Args[1:], conf)
	if err != nil {
		engine.Panic(err)
	}

	if effects.ShowHelp {
		fmt.Fprintf(os.Stderr, "Usage: %v [-v -f -h]\n", os.Args[0])
		f.PrintDefaults()
		os.Exit(0)
	}

	if effects.Verbose {
		SetDebug(DbgFlagAll)
	}
}

func getFieldPtrOrAlloc(g *Game, v reflect.Value, i int) (name string, val any) {
	return coreproject.FieldPtrOrAlloc(v, i, coreproject.FieldAllocConfig{
		IsPointerSpriteType: func(typ reflect.Type) bool {
			return typ.Implements(tySprite)
		},
		ResolveInterfaceSpriteType: func(fieldName string) (reflect.Type, bool) {
			typ, ok := g.typs[fieldName]
			return typ, ok
		},
	})
}
