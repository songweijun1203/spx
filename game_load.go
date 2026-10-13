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
	"maps"
	"math"
	"reflect"
	"slices"
	"unsafe"

	"github.com/goplus/spx/v3/internal/base/defaults"
	coreproject "github.com/goplus/spx/v3/internal/core/project"
	"github.com/goplus/spx/v3/internal/engine"
	"github.com/goplus/spx/v3/internal/engine/platform"
	spxlog "github.com/goplus/spx/v3/internal/log"
	"github.com/goplus/spx/v3/internal/ui"
)

type spriteLoader func(sprite Sprite, name string, gamer reflect.Value) error

type preparedSprite struct {
	config coreproject.SpriteConfig
	layout *coreproject.CostumeLayout
}

// loadSprite 读取并预处理一个精灵的配置，然后初始化对应的 Go SpriteImpl。
// 它不会执行 awake/Main；脚本生命周期统一由 runSpriteCallbacks 排队。
func (p *Game) loadSprite(sprite Sprite, name string, gamer reflect.Value) error {
	spxlog.Debug("LoadSprite: %s", name)
	loaded, err := coreproject.LoadSpriteConfig(p.fs, name)
	if err != nil {
		return err
	}
	prepared, err := prepareSpriteConfig(&loaded.Config)
	if err != nil {
		return fmt.Errorf("sprite config %q: %w", name, err)
	}
	return p.loadPreparedSprite(sprite, name, gamer, &prepared)
}

// prepareSpriteConfig 在创建运行代理前解析服装布局和动画帧引用。
// 返回副本，避免把派生字段写回共享的项目配置对象。
func prepareSpriteConfig(config *coreproject.SpriteConfig) (preparedSprite, error) {
	layout, err := coreproject.PrepareCostumeLayout(config)
	if err != nil {
		return preparedSprite{}, err
	}
	preparedAnimations, err := prepareFrameAnimations(config.FAnimations, layout)
	if err != nil {
		return preparedSprite{}, err
	}
	preparedConfig := *config
	preparedConfig.FAnimations = preparedAnimations
	return preparedSprite{config: preparedConfig, layout: layout}, nil
}

// prepareFrameAnimations 按名称稳定遍历动画，把服装名/索引解析为布局中的帧索引，
// 并补齐运行时默认值和派生时长。
func prepareFrameAnimations(animations map[string]*coreproject.AniConfig, layout *coreproject.CostumeLayout) (map[string]*coreproject.AniConfig, error) {
	if animations == nil {
		return nil, nil
	}
	prepared := make(map[string]*coreproject.AniConfig, len(animations))
	for _, name := range slices.Sorted(maps.Keys(animations)) {
		animation := animations[name]
		if animation == nil {
			return nil, fmt.Errorf("fAnimations[%q] is null", name)
		}
		frameFrom, err := prepareAnimationFrame(name, "frameFrom", animation.FrameFrom, layout)
		if err != nil {
			return nil, err
		}
		frameTo, err := prepareAnimationFrame(name, "frameTo", animation.FrameTo, layout)
		if err != nil {
			return nil, err
		}

		definition := *animation
		defaults.SetDefaultIfZero(&definition.FrameFps, 25)
		defaults.SetDefaultIfZero(&definition.TurnToDuration, 1.0)
		defaults.SetDefaultIfZero(&definition.StepDuration, 0.01)
		definition.IFrameFrom = frameFrom
		definition.IFrameTo = frameTo
		definition.Speed = 1
		definition.Duration = (math.Abs(float64(frameFrom-frameTo)) + 1) / float64(definition.FrameFps)
		prepared[name] = &definition
	}
	return prepared, nil
}

// prepareAnimationFrame 校验单个动画端点，错误信息保留动画名和字段名。
func prepareAnimationFrame(animation, field string, value any, layout *coreproject.CostumeLayout) (int, error) {
	index, ok := layout.ResolveFrameIndex(value)
	if !ok {
		return 0, fmt.Errorf("fAnimations[%q].%s references missing costume %q", animation, field, value)
	}
	costumeCount := len(layout.Frames)
	if index < 0 || index >= costumeCount {
		return 0, fmt.Errorf("fAnimations[%q].%s index %d is outside %d costumes", animation, field, index, costumeCount)
	}
	return index, nil
}

// loadPreparedSprite 把预处理配置写入业务精灵，建立组件和 Godot 代理，
// 最后把它登记为该精灵类型的项目主实例。
func (p *Game) loadPreparedSprite(sprite Sprite, name string, gamer reflect.Value, prepared *preparedSprite) error {
	// 生成精灵结构的第 0 个字段约定为嵌入的 SpriteImpl。先清零整个对象，
	// 避免 reload 沿用旧组件、事件 owner 或运行代理。
	vSpr := reflect.ValueOf(sprite).Elem()
	vSpr.Set(reflect.Zero(vSpr.Type()))
	base := vSpr.Field(0).Addr().Interface().(*SpriteImpl)
	// 配置路径已经由 LoadSpriteConfig 归一化到项目 assets 根。
	config := &prepared.config
	// 先建立服装与图集帧，再保存默认服装索引并绑定脚本事件 owner。
	base.initSpriteCostumes(config, prepared.layout)
	base.spriteState.DefaultCostumeIndex = base.costumeIndex
	base.scriptEventBindings.bind(&p.scriptEvents, base)

	base.gamer = gamer
	base.g, base.name, base.sprite = p, name, sprite
	base.runtimeState.Scale = config.Size
	base.spriteState.IsVisible = config.Visible

	base.components.initComponents(base, config)
	base.initRuntimeProxy()
	// 舞台 Z 序再次引用该名称时复用这个主实例；特殊 sprites 条目以它为模板。
	p.sprs[name] = sprite
	// 生成精灵的第 1 个字段约定为 *Game，回填后精灵脚本可访问舞台 API。
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
	entries []preparedStageEntry,
) {
	// 显示尺寸必须先于相机和代理创建；Tilemap 在相机建立后修正最终舞台几何。
	p.setupDisplayConfig(proj)
	p.setupWorldAndWindow(proj)
	p.setupPlatformAndCamera(proj)
	p.setupAudioAndTilemap(proj)

	inits := p.loadAndInitSprites(g, proj, loadSprite, entries)
	// 不在加载调用栈中执行 awake/Main，保证舞台对象和基础系统已完整可见。
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
// runSpriteCallbacks 按碰撞数据、awake、精灵 Main、游戏 OnLoaded 的顺序，
// 把舞台初始化期间不能立即执行的脚本工作登记到当前 bootstrap 代际。
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
	// Tilemap/装饰物可能按瓦片范围修正世界尺寸；applyStageGeometry 随后更新
	// Camera2D 限制和 Pen SubViewport 尺寸。
	p.applyTilemap()
	// SoundObj 是舞台级音效参数和播放 owner；配置与媒体仍在首次播放时惰性加载。
	p.ensureSoundObject(&p.audioState.SoundObj)
	if proj.Bgm != "" {
		// BGM 是启动阶段唯一主动触发的声音。
		p.Play__1(proj.Bgm, true)
	}
}

func (p *Game) applyTilemap() {
	p.tilemapMgr.parseTilemap()
	p.applyStageGeometry()
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
