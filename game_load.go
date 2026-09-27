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
	"fmt"
	"reflect"
	"unsafe"

	coreproject "github.com/goplus/spx/v3/internal/core/project"
	"github.com/goplus/spx/v3/internal/engine"
	"github.com/goplus/spx/v3/internal/engine/platform"
	spxlog "github.com/goplus/spx/v3/internal/log"
	"github.com/goplus/spx/v3/internal/ui"
)

type spriteLoader func(sprite Sprite, name string, gamer reflect.Value) error

// loadSprite 读取一个精灵的配置，并初始化对应的 Go SpriteImpl。
// 直接调用方：loadGameSprites()、loadStage() 的精灵加载回调。
// 它不会执行精灵 awake/Main；脚本生命周期由 runSpriteCallbacks() 排队。
func (p *Game) loadSprite(sprite Sprite, name string, gamer reflect.Value) error {
	spxlog.Debug("LoadSprite: %s", name)
	loaded, err := coreproject.LoadSpriteConfig(p.fs, name)
	if err != nil {
		return err
	}
	return p.loadSpriteConfig(sprite, name, gamer, &loaded.Config)
}

// loadSpriteConfig 把已经解析好的 SpriteConfig 写入 Go 精灵对象，
// 并初始化 SpriteImpl、组件和 Godot 运行代理，最后登记到 p.sprs。
func (p *Game) loadSpriteConfig(sprite Sprite, name string, gamer reflect.Value, cfg *coreproject.SpriteConfig) error {
	// 生成精灵结构的第 0 个字段约定为嵌入的 SpriteImpl。先把整个对象清零，避免
	// reload 时沿用旧组件、事件 owner 或 Godot 代理，再从同一个业务对象地址重建状态。
	vSpr := reflect.ValueOf(sprite).Elem()
	vSpr.Set(reflect.Zero(vSpr.Type())) //结构体清零
	base := vSpr.Field(0).Addr().Interface().(*SpriteImpl)
	// cfg 中的资源路径已经由 coreproject.LoadSpriteConfig 归一化到项目 assets 根。
	base.init(p, name, cfg, gamer, sprite)
	// sprs 是“精灵类型名 -> 项目原型/主实例”的缓存。舞台 Z 序再次引用该名称时
	// 会复用这里的对象；特殊的 sprites 数组则以它为模板复制出多个独立实例。
	p.sprs[name] = sprite
	// 生成精灵的第 1 个字段约定为 *Game；回填后精灵脚本可直接访问舞台 API/字段。
	return bindSpriteOwner(vSpr, gamer)
}

// loadStage 根据 project 配置建立舞台级状态。
// 它同步完成显示、窗口、平台、相机、音频和 Tilemap 的准备，
// 再创建 Z 序中的精灵/特殊形状；脚本回调则交给 bootstrap 队列。
func (p *Game) loadStage(
	g reflect.Value,
	proj *coreproject.ProjectConfig,
	generation uint64,
	loadSprite spriteLoader,
) {
	p.setupDisplayConfig(proj)
	p.setupWorldAndWindow(proj)
	p.setupPlatformAndCamera(proj) //内部会创建背景节点
	p.setupAudioAndTilemap(proj)

	inits := p.loadAndInitSprites(g, proj, loadSprite)
	// 这里不直接执行精灵 awake/Main，而是登记到 bootstrap 队列，保证舞台和基础系统准备好后再运行。
	p.runSpriteCallbacks(inits, proj, g, generation)
}

// -----------------------------------------------------------------------------
// Display Setup
// -----------------------------------------------------------------------------
func (p *Game) setupDisplayConfig(proj *coreproject.ProjectConfig) {
	// ResolveDisplaySettings 负责默认值；这里保存 Go 镜像并立即把 debug 模式写给 Godot。
	display := coreproject.ResolveDisplaySettings(proj)
	p.displayState.WindowScale = display.WindowScale
	p.displayState.StretchMode = display.StretchMode
	p.debugState.Debug = display.Debug
	if p.debugState.Debug {
		spxlog.SetLevel(spxlog.LevelDebug)
	} else {
		spxlog.SetLevel(spxlog.LevelInfo)
	}
	engine.SetDebugMode(p.debugState.Debug)
}

func (p *Game) applyWorldWindowMetrics(metrics coreproject.WorldWindowMetrics) {
	p.displayState.WorldWidth = metrics.WorldWidth
	p.displayState.WorldHeight = metrics.WorldHeight
	p.displayState.MinWorldX = metrics.MinWorldX
	p.displayState.MinWorldY = metrics.MinWorldY
	p.displayState.MapMode = metrics.MapMode
	p.displayState.WindowWidth = metrics.WindowWidth
	p.displayState.WindowHeight = metrics.WindowHeight
}

func (p *Game) setupWorldAndWindow(proj *coreproject.ProjectConfig) {
	// TileMap 存在时地图尺寸优先；没有 TileMap 时回退项目 Map 或 480x360 基准尺寸。
	proj.Map = coreproject.ResolveMapConfig(proj.Map, p.tilemapMgr.hasData(), baseScreenWidth, baseScreenHeight)
	backdrops := proj.GetBackdrops()
	if p.tilemapMgr.hasData() {
		backdrops = make([]*coreproject.BackdropConfig, 0)
	}

	p.displayState.WorldWidth = proj.Map.Width
	p.displayState.WorldHeight = proj.Map.Height

	if len(backdrops) > 0 {
		// 背景配置会形成 Game.baseObj 的服装列表；doWorldSize 会结合当前背景尺寸修正世界。
		p.baseObj.initBackdrops(backdrops, proj.GetBackdropIndex())
		p.doWorldSize()
	} else {
		p.baseObj.initWithSize(p.displayState.WorldWidth, p.displayState.WorldHeight)
	}
	spxlog.Debug("SetWorldSize: %d, %d", p.displayState.WorldWidth, p.displayState.WorldHeight)

	p.doWindowSize()
	metrics := coreproject.ResolveWorldWindowMetrics(
		p.displayState.WorldWidth,
		p.displayState.WorldHeight,
		p.displayState.WindowWidth,
		p.displayState.WindowHeight,
		coreproject.ToMapMode(proj.Map.Mode),
	)
	p.applyWorldWindowMetrics(metrics)
	spxlog.Debug("SetWindowSize: %d, %d", p.displayState.WindowWidth, p.displayState.WindowHeight)
}

func (p *Game) setupPlatformAndCamera(proj *coreproject.ProjectConfig) {
	platformMgr := engine.Managers().PlatformMgr

	// 平台布局把项目窗口、全屏、移动端和当前宿主窗口状态合并成最终尺寸。
	layout := coreproject.ResolvePlatformLayout(coreproject.PlatformLayoutInput{
		WindowWidth:       p.displayState.WindowWidth,
		WindowHeight:      p.displayState.WindowHeight,
		WindowScale:       p.displayState.WindowScale,
		Fullscreen:        proj.FullScreen,
		IsMobile:          platform.IsMobile(),
		IsWeb:             platform.IsWeb(),
		CurrentWindowSize: platformMgr.GetWindowSize(),
	})
	if layout.Fullscreen {
		platformMgr.SetWindowFullscreen(true)
	}
	p.displayState.WindowScale = layout.WindowScale
	platformMgr.SetWindowSize(layout.WindowWidth, layout.WindowHeight, true)
	platformMgr.SetMaxFps(int64(proj.MaxFPS))
	platformMgr.SetStretch(p.displayState.StretchMode, layout.ContentWidth, layout.ContentHeight)

	p.camera = &cameraImpl{}
	p.Camera = p.camera
	// camera.init 经 CameraMgr 创建/取得 Godot Camera2D，并建立 Go 相机状态。
	p.camera.init(p)

	isWindowMapSizeEqual := coreproject.IsWindowWorldSizeEqual(
		p.displayState.WorldWidth,
		p.displayState.WorldHeight,
		p.displayState.WindowWidth,
		p.displayState.WindowHeight,
	)
	engine.SetWindowScale(p.displayState.WindowScale)
	ui.SetBaseScreenSize(baseScreenWidth, baseScreenHeight)
	ui.ClampUIPositionInScreen(isWindowMapSizeEqual)

	// Game 自身也使用一个 internal/engine.Sprite 代理表示舞台背景。Godot 侧该 SpxSprite
	// 位于 sprite_root，z=-1、无物理；业务精灵从 firstSpriteLayer 开始排列。
	p.runtimeState.SyncSprite = engine.NewBackdropProxy(p, p.getCostumePath(), p.getCostumeRenderScale())
	p.setupBackdrop() //会设置背景和缩放
}

func (p *Game) syncPenCanvasToWorld() {
	width := int64(p.displayState.WorldWidth)
	height := int64(p.displayState.WorldHeight)
	p.penCommandBarrier(func() {
		engine.Managers().PenMgr.SetCanvasSize(width, height)
	})
}

func (p *Game) applyStageGeometry() {
	if p.camera != nil {
		p.camera.setLimits()
	}
	p.syncPenCanvasToWorld()
}

// -----------------------------------------------------------------------------
// Sprite Setup
// -----------------------------------------------------------------------------
// loadAndInitSprites 按项目 Z 序加载舞台精灵和特殊形状，设置渲染层。
// 返回值是后续需要执行 awake/Main 的精灵列表。
func (p *Game) loadAndInitSprites(
	g reflect.Value,
	proj *coreproject.ProjectConfig,
	loadSprite spriteLoader,
) []Sprite {
	inits := make([]Sprite, 0, len(proj.Zorder))
	// WalkZOrder 严格按 project.zorder 顺序展开普通名称和特殊 shape 条目。
	// 普通名称复用 p.sprs 中已加载的主实例；特殊 sprites 条目会从原型复制新实例。
	err := coreproject.WalkZOrder(
		proj.Zorder,
		func(layer int, name string) error {
			sp := p.getSpriteProtoByName(name, g, loadSprite)
			spr := spriteOf(sp)
			spr.setLayer(layer + firstSpriteLayer)
			// 加入 shapeMgr 后对象才参与查询、逐帧同步、输入命中和销毁管理。
			p.addShape(spr)
			inits = append(inits, sp)
			return nil
		},
		func(layer int, shape coreproject.StageShape) error {
			var err error
			inits, err = p.addSpecialShape(g, shape, inits, loadSprite)
			if err != nil {
				return fmt.Errorf("addSpecialShape: %w", err)
			}
			return nil
		},
	)
	if err != nil {
		engine.Panic(err)
	}
	// 特殊数组可能把一个 Z 序条目展开成多个精灵，因此最终按 shapeMgr 实际顺序
	// 重新分配连续图层；精灵层位于共享画笔画布之上。
	p.shapeMgr.updateRenderLayers()
	return inits
}

// runSpriteCallbacks 把舞台初始化期间不能立即执行的生命周期工作登记到
// bootstrap 队列，顺序为碰撞数据、精灵 awake、精灵 Main、游戏 OnLoaded。
// 这样项目脚本会在舞台对象和基础系统准备好之后再运行。
func (p *Game) runSpriteCallbacks(inits []Sprite, proj *coreproject.ProjectConfig, g reflect.Value, generation uint64) {
	var onLoaded func()
	if loader, ok := g.Addr().Interface().(interface{ OnLoaded() }); ok {
		onLoaded = loader.OnLoaded
	}
	queueBootstrap := func(call func()) {
		p.queueBootstrap(generation, call)
	}
	// bootstrap 中的脚本可以再次改相机目标；这里先应用项目配置给出的初始跟随对象。
	if proj.Camera != nil && proj.Camera.On != "" {
		p.Camera.Follow__1(proj.Camera.On)
	}
	queueBootstrap(func() {
		p.setupCollisionData(inits)
	})
	for _, ini := range inits {
		if spr := spriteOf(ini); spr != nil {
			queueBootstrap(spr.awake)
		}
	}
	queueBootstrap(func() {
		runSpriteMainsUntilYield(inits)
	})
	queueBootstrap(onLoaded)
}

// -----------------------------------------------------------------------------
// Stage Items
// -----------------------------------------------------------------------------
func (p *Game) setupAudioAndTilemap(proj *coreproject.ProjectConfig) {
	// TileMap/装饰物会创建 Godot 节点，并可能据实际瓦片范围修正世界尺寸；修正后
	// applyStageGeometry 再更新 Camera2D 限制和 Pen SubViewport 尺寸。
	p.applyTilemap()
	// SoundObj 是舞台级音效参数/播放 owner。声音配置与媒体仍按第一次播放惰性加载。
	p.audioState.SoundObj = p.soundMgr.AllocSound()
	if proj.Bgm != "" {
		// BGM 是启动加载阶段唯一主动触发的声音；Play 会按路径加载 Godot AudioStream。
		p.Play__1(proj.Bgm, true)
	}
}

func (p *Game) applyTilemap() {
	p.tilemapMgr.parseTilemap()
	p.applyStageGeometry()
}

func (p *Game) addSpecialShape(
	g reflect.Value,
	v coreproject.StageShape,
	inits []Sprite,
	loadSprite spriteLoader,
) ([]Sprite, error) {
	// 特殊 shape 不是单一类型：monitor/measure 直接创建 UI/Shape；sprite 引用已有
	// Gamer 字段；sprites 从一个精灵原型展开多个实例。只有返回到 inits 的精灵才会
	// 进入后续 awake/Main/collision bootstrap。
	return coreproject.AppendStageItems(inits, v, coreproject.StageItemHandlers[Sprite]{
		StageMonitor: func(shape coreproject.StageShape) error {
			sm, err := newMonitor(g, shape)
			if err != nil {
				spxlog.Error("AddSpecialShape type: %s", shape["type"])
				return nil
			}
			p.shapeMgr.addShape(sm)
			return nil
		},
		Measure: func(shape coreproject.StageShape) error {
			p.shapeMgr.addShape(ui.NewMeasureShape(shape))
			return nil
		},
		Sprites: func(shape coreproject.StageShape) ([]Sprite, error) {
			return p.addStageSprites(g, shape, loadSprite)
		},
		Sprite: func(shape coreproject.StageShape) (Sprite, error) {
			return p.addStageSprite(g, shape)
		},
	})
}

func (p *Game) addStageSprite(g reflect.Value, v coreproject.StageShape) (Sprite, error) {
	target, err := stageShapeTarget(v)
	if err != nil {
		return nil, err
	}
	var added Sprite
	err = coreproject.BindStageSprite(g, target, coreproject.FindObjectPtr, func(val any) error {
		sp, ok := val.(Sprite)
		if !ok {
			return fmt.Errorf("stage sprite target is not a sprite")
		}
		dest := spriteOf(sp)
		applySpriteProps(dest, v)
		p.shapeMgr.addShape(dest)
		added = sp
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("addStageSprite: %w", err)
	}
	return added, nil
}

func (p *Game) addStageSprites(
	g reflect.Value,
	v coreproject.StageShape,
	loadSprite spriteLoader,
) ([]Sprite, error) {
	target, err := stageShapeTarget(v)
	if err != nil {
		return nil, err
	}
	rawItems, err := stageShapeItems(v)
	if err != nil {
		return nil, err
	}
	items := make([]Sprite, 0, len(rawItems))
	err = coreproject.BindStageSprites(
		g,
		target,
		rawItems,
		coreproject.FindFieldPtr,
		func(typ reflect.Type) bool {
			return typ.Implements(tySprite)
		},
		func(newItem reflect.Value, shape coreproject.StageShape) error {
			spr := p.getSpriteProto(newItem.Type(), g, loadSprite)
			dest, sp := applySprite(newItem, spr, shape)
			p.shapeMgr.addShape(dest)
			items = append(items, sp)
			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("addStageSprites: %w", err)
	}
	return items, nil
}

func bindSpriteOwner(spriteValue reflect.Value, gamer reflect.Value) error {
	// XGo 生成结构约定：字段 0 是 SpriteImpl，字段 1 是 *Game owner。
	if spriteValue.NumField() < 2 {
		return fmt.Errorf("sprite %s is missing owner field", spriteValue.Type())
	}

	ownerField := spriteValue.Field(1)
	if !ownerField.CanAddr() {
		return fmt.Errorf("sprite %s owner field is not addressable", spriteValue.Type())
	}

	gamerPtr := gamer.Addr()
	if !gamerPtr.Type().AssignableTo(ownerField.Type()) {
		return fmt.Errorf(
			"sprite %s owner field type %s cannot hold %s",
			spriteValue.Type(), ownerField.Type(), gamerPtr.Type(),
		)
	}

	// ownerField may be unexported in generated sprite structs, so Set would panic.
	// reflect.NewAt gives us a typed, settable view over the same storage.
	reflect.NewAt(ownerField.Type(), unsafe.Pointer(ownerField.UnsafeAddr())).Elem().Set(gamerPtr)
	return nil
}

func stageShapeTarget(shape coreproject.StageShape) (string, error) {
	target, ok := shape["target"].(string)
	if !ok || target == "" {
		return "", fmt.Errorf("stage shape target must be a non-empty string")
	}
	return target, nil
}

func stageShapeItems(shape coreproject.StageShape) ([]any, error) {
	items, ok := shape["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("stage shape items must be an array")
	}
	return items, nil
}
