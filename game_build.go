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
	// 阶段 1：打开项目资源视图。普通工程读取 assets/index.json；若存在
	// index_pack.json，则包装成“打包配置优先、缺失子项回退源文件”的统一 FS。
	// opened.FS 会保存在 Game.fs 中供运行期按需加载声音，所以这里不能立即 Close。
	opened, err := coreproject.OpenBuilderResources(resource, nil)
	if err != nil {
		return err
	}
	// 阶段 2：建立逻辑资源路径到 Godot/文件系统路径的转换根。
	if opened.AssetDir != "" {
		engine.SetAssetDir(opened.AssetDir)
	}
	// 字体必须先于 UI、气泡和监视器创建应用，否则先创建的 Control 会继承旧主题字体。
	// ApplyProjectFonts 是 Godot 资源操作；从当前加载 Thread 调用时会同步切到主线程。
	fontPlan := coreproject.ResolveRuntimeFontPlan(opened.Fonts, engine.ToAssetPath)
	if err := applyRuntimeFontPlan(&engine.Managers().ResMgr, fontPlan); err != nil {
		return fmt.Errorf("apply project fonts: %w", err)
	}

	conf, proj := &opened.Config, &opened.Project
	// 阶段 3：合并命令行/项目运行参数，并配置不依赖具体舞台实例的系统级选项。
	parseCommandLineFlags(conf)
	p.applyRuntimeConfig(conf, proj)
	setupGameSystems(p, proj)
	// 阶段 4：反射 Gamer 字段，读取每种精灵的配置并初始化 Go SpriteImpl。
	// SpriteImpl.init 同时会经 SpriteMgr 创建 Godot SpxSprite 代理，但尚不执行脚本 Main。
	gamer := reflect.ValueOf(p.gamer).Elem()
	// g：SPX 运行时的基础 *Game。
	// v：生成代码中具体游戏结构的反射值。
	// fs：统一资源视图，可能来自普通 index.json，也可能包含 index_pack.json。
	// proj：已经解析好的项目级 assets/index.json。
	loadGameSprites(p, gamer, opened.FS, proj)
	// 阶段 5：创建背景、相机、TileMap、舞台实例和 Z 序，并把脚本生命周期工作
	// 排入 bootstrap。loadStage 返回时，舞台对象已经存在，但用户脚本尚未全部执行。
	p.loadStage(gamer, proj, generation, p.loadSprite)

	platform := &engine.Managers().PlatformMgr
	// 阶段 6：启动三个长期循环 Thread（事件、输入、逻辑），再设置平台运行策略。
	// 这些 Thread 创建后会先竞争 runMu，主体通常立即在通道/下一帧等待点挂起。
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
