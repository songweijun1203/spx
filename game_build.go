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
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"

	spxfs "github.com/goplus/spx/v3/fs"
	coreproject "github.com/goplus/spx/v3/internal/core/project"
	"github.com/goplus/spx/v3/internal/engine"
)

// loadGame 在 OnEngineStart 的启动任务中加载项目运行时数据。
//
// 它打开 project/assets 数据，校验并应用项目配置，建立舞台和精灵的 Go 状态，
// 再通过 SpriteMgr 创建 Godot 运行代理。awake、精灵 Main 和 OnLoaded 由
// loadStage 登记到 bootstrap 队列，不在本函数的当前调用栈中执行。
//
// resource 通常是 "assets"；generation 用于拒绝上一局遗留的异步启动工作。
func (p *Game) loadGame(resource any, generation uint64) (err error) {
	// 阶段 1：打开统一项目资源视图。成功转交给 Game.fs 后由游戏生命周期关闭；
	// 若后续初始化失败，则由下面的 defer 立即释放。
	opened, err := coreproject.OpenBuilderResources(resource, nil)
	if err != nil {
		return err
	}
	ownsResources := true
	defer func() {
		if !ownsResources {
			return
		}
		if closeErr := opened.FS.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close project resources: %w", closeErr))
		}
	}()

	conf, proj := &opened.Config, &opened.Project
	// 阶段 2：合并命令行参数，并在创建任何运行对象前校验结构化项目配置。
	parseCommandLineFlags(conf)
	if err := validateProjectConfig(proj); err != nil {
		return fmt.Errorf("project config: %w", err)
	}
	if opened.AssetDir != "" {
		engine.SetAssetDir(opened.AssetDir)
	}
	// 字体必须先于 UI、气泡和监视器应用，否则先创建的 Control 会继承旧主题字体。
	fontPlan := coreproject.ResolveRuntimeFontPlan(opened.Fonts, engine.ToAssetPath)
	if err := applyRuntimeFontPlan(&engine.Managers().ResMgr, fontPlan); err != nil {
		return fmt.Errorf("apply project fonts: %w", err)
	}

	// 阶段 3：配置显示、物理、音频、寻路和图层排序等系统级选项。
	p.applyRuntimeConfig(conf, proj)
	setupGameSystems(p, proj)
	// 阶段 4：反射 Gamer 字段，预处理每种精灵配置并初始化 Go SpriteImpl。
	gamer := reflect.ValueOf(p.gamer).Elem()
	loadGameSprites(p, gamer, opened.FS, proj)
	// startLoad 已把 opened.FS 保存到 Game；从这里起资源所有权随本局生命周期转移。
	ownsResources = false
	// 阶段 5：创建背景、相机、Tilemap、舞台实例和 Z 序，并排入脚本生命周期工作。
	p.loadStage(gamer, proj, generation, p.loadSprite, nil)

	platform := &engine.Managers().PlatformMgr
	// 阶段 6：创建长期事件/输入/逻辑协程，再应用宿主窗口策略。
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
	// Go 音频 Manager 保存 Godot AudioMgr 代理，并初始化播放实例跟踪表；此处不预载音频。
	p.soundMgr.Init(&engine.Managers().AudioMgr)
	// 声音配置采用按名称惰性加载，缓存从空表开始。
	p.sounds = make(map[string]sound)
	// 输入 Manager 建立本局鼠标/点击目标等状态。
	p.inputMgr.init(p)
	// eventLoop 消费该通道；脚本事件不会在 Godot 原始回调栈中直接执行。
	p.events = make(chan event, eventBufferSize)
	p.eventQueueState.EventQueueStats.Reset()
	p.fs = fs
}

// -----------------------------------------------------------------------------
// Setup
// -----------------------------------------------------------------------------
// setupGameSystems 应用项目级物理、音频、路径查找和渲染层排序配置。
// 这里配置运行系统，不执行精灵生命周期脚本。
func setupGameSystems(g *Game, proj *coreproject.ProjectConfig) {
	settings := coreproject.ResolveSystemSettings(proj)
	engine.SetLayerSortMode(settings.LayerSortMode)
	g.applyPathFinderSettings(settings)
	g.applyAudioSettings(settings)
	g.applyPhysicsSettings(settings)
}

func validateProjectConfig(project *coreproject.ProjectConfig) error {
	for i, backdrop := range project.Backdrops {
		if backdrop == nil {
			return fmt.Errorf("backdrops[%d] is null", i)
		}
	}
	settings := coreproject.ResolveSystemSettings(project)
	if settings.AutoSetCollisionLayer == project.Physics {
		return fmt.Errorf("autoSetCollisionLayer and physics must have different enabled states")
	}
	return nil
}

// -----------------------------------------------------------------------------
// Loading
// -----------------------------------------------------------------------------
// loadGameSprites 遍历 Gamer 的 Go 字段，加载字段对应的精灵配置，
// 并初始化项目精灵原型和 Tilemap 数据。
func loadGameSprites(g *Game, v reflect.Value, fs spxfs.Dir, proj *coreproject.ProjectConfig) {
	g.startLoad(fs)
	// WalkFields 遍历生成的 Gamer 结构字段。getFieldPtrOrAlloc 会：
	//   1. 对 *ConcreteSprite 字段分配缺失对象；
	//   2. 对 Sprite 接口字段按字段名从 g.typs 找到具体类型；
	//   3. 返回“字段名 + 指针值”。
	// 回调只处理同时实现 Sprite 且已由 initGame 登记类型的字段。这里通常会把
	// 生成代码预先放入字段的对象清零并重新按项目配置初始化，而不是另建业务身份。
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
	// 精灵配置读取后只解析 TileMap 的描述数据/调用新格式解析器；实际铺瓦片和装饰物
	// 要到 loadStage.setupAudioAndTilemap，届时世界、窗口和场景根节点均已可用。
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
