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
	"reflect"
	"sync"
	"sync/atomic"

	spxfs "github.com/goplus/spx/v3/fs"
	_ "github.com/goplus/spx/v3/fs/asset"
	_ "github.com/goplus/spx/v3/fs/zip"

	"github.com/goplus/spx/v3/internal/audio"
	"github.com/goplus/spx/v3/internal/base/collision"
	corestate "github.com/goplus/spx/v3/internal/core/state"
	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine"
	spxlog "github.com/goplus/spx/v3/internal/log"
	itime "github.com/goplus/spx/v3/internal/time"
)

const (
	XGoPackage = true
)

const (
	Gop_sched = "Sched,SchedNow"
)

var (
	gco      *coroutine.Coroutines
	tySprite = reflect.TypeFor[Sprite]()
)

var defaultDebugFlags atomic.Uint32

type dbgFlags int

const (
	DbgFlagLoad dbgFlags = 1 << iota
	DbgFlagInstr
	DbgFlagEvent
	DbgFlagPerf
	DbgFlagAll = DbgFlagLoad | DbgFlagInstr | DbgFlagEvent | DbgFlagPerf
)

const (
	MOUSE_BUTTON_LEFT   int64 = 1
	MOUSE_BUTTON_RIGHT  int64 = 2
	MOUSE_BUTTON_MIDDLE int64 = 3
)

const (
	eventBufferSize             = 16
	schedTimeoutMs              = 3000
	mainExecTimeoutSec          = 3
	mouseMovementThreshold      = 1.0
	initialSpriteSyncBufferSize = 100
	initialPenSyncBufferSize    = 256
	baseScreenWidth             = 480
	baseScreenHeight            = 360
)

// Game represents the main game instance with all core systems.
type Game struct {
	// 嵌入游戏/舞台自身的通用对象状态：服装、图形特效、运行代理和图层。
	// 主要使用方：loadStage()、runtime_display.go、runtime_sync.go、game_stage.go。
	baseObj
	// 嵌入脚本事件绑定能力，使 Game 可以注册和接收按键、碰撞、广播等事件。
	// 主要使用方：bindScriptEvents()、runtime_events.go、Game.OnEngineUpdate()。
	scriptEventBindings
	// 当前项目的文件系统视图；由 startLoad() 设置，供精灵、声音和 Tilemap 读取配置。
	// 主要使用方：loadSprite()、LoadSoundConfig()、tilemapMgr.loadMap()。
	fs spxfs.Dir

	// 本局生命周期状态：一次性启动、bootstrap 完成、Start 事件派发和可运行状态。
	// 主要使用方：Game.OnEngineStart()、OnEngineBeforeUpdate()、OnEngineUpdate()、reset()。
	lifecycleState corestate.GameLifecycleState
	// 世界、窗口、地图模式、缩放和拉伸等显示状态。
	// 主要使用方：setupDisplayConfig()、setupWorldAndWindow()、setupPlatformAndCamera()、runtime_display.go。
	displayState corestate.GameDisplayState
	// Ask 对话框及其回答值。
	// 主要使用方：Ask()、Answer()、reset()。
	dialogState corestate.GameDialogState
	// 调试开关和调试面板状态。
	// 主要使用方：setDebugFlags()、setupDisplayConfig()、debug.go、reset()。
	debugState corestate.GameDebugState
	// 游戏事件队列的锁、容量策略和统计数据。
	// 主要使用方：initEventQueueState()、queueEventWithPolicy()、runtime_events.go、runtime_queue.go。
	eventQueueState corestate.GameEventQueueState
	// 路径查找网格的单元尺寸。
	// 主要使用方：applyPathFinderSettings()、路径查找初始化和寻路 API。
	pathfindingState corestate.GamePathfindingState
	// 游戏级音频衰减、最大距离和共享声音对象 ID。
	// 主要使用方：setupAudioAndTilemap()、game_sound.go、releaseGameAudio()。
	audioState corestate.GameAudioState
	// 从项目配置解析出的运行时配置副本，例如窗口标题和焦点行为。
	// 主要使用方：applyRuntimeConfig()、loadGame()、PlatformMgr.SetWindowTitle()。
	runtimeConfigInput Config
	// 项目是否启用物理系统；参与碰撞层配置和物理 Manager 初始化。
	// 主要使用方：applyRuntimeConfig()、setupGameSystems()、game_physics.go。
	physicsEnabled bool

	// 对外暴露的相机接口；由 setupPlatformAndCamera() 创建，项目脚本通过它控制相机。
	// 主要使用方：Camera.Follow__1()、相机相关脚本 API。
	Camera Camera
	// Camera 接口对应的内部实现，负责每帧相机更新、边界和脏标记。
	// 主要使用方：setupPlatformAndCamera()、runtime_sync.go、runtime_display.go。
	camera *cameraImpl

	// 精灵名称到 Go 反射类型的映射；由 initGame() 登记，loadStage() 按名称创建精灵。
	// 主要使用方：initGame()、getSpriteProtoByName()、buildSpriteCollisionInfos()。
	typs map[string]reflect.Type
	// 已加载的精灵原型/实例表，按名称复用，reset() 时清空。
	// 主要使用方：loadSpriteConfig()、getSpriteProto()、getSpriteProtoByName()、reset()。
	sprs map[string]Sprite
	// 按声音名称缓存的 Go 声音对象。
	// 主要使用方：startLoad()、game_sound.go、reset()。
	sounds map[string]sound

	// 项目事件通道；由 startLoad() 创建，runtime_loops.go 从中消费事件。
	// 主要使用方：startLoad()、queueEventWithPolicy()、initEventLoop()。
	events chan event

	// 当前游戏的脚本事件注册表、事件队列和脚本线程执行状态。
	// 主要使用方：bindScriptEvents()、runtime_events.go、runFrameScripts()。
	scriptEvents scriptEventRegistry
	// 生成的项目 Gamer 对象；由 XGot_Game_Main() 写入，提供 MainEntry() 和项目字段。
	// 主要使用方：Game.OnEngineStart()、loadGame()、game_stage.go。
	gamer Gamer
	// 保护 bootstrap 代际、任务队列和启动标记的互斥锁。
	// 主要使用方：queueBootstrap()、resetBootstrap()、markGameStarted()、claimBootstrap()。
	bootstrapMu sync.Mutex
	// 当前 bootstrap 生命周期编号；reset 时递增，用于丢弃旧异步任务。
	// 主要使用方：bootstrapGeneration()、isCurrentBootstrap()、completeBootstrap()。
	bootstrapGen uint64
	// 当前代际是否已经抢到 bootstrap 执行权。
	// 主要使用方：claimBootstrap()、startBootstrap()。
	bootstrapStarted bool
	// 是否已经安排项目 Start 事件，防止同一局重复排队。
	// 主要使用方：scheduleStartEvent()、resetBootstrap()。
	startScheduled bool
	// 等待 bootstrap 执行的函数队列，保存 MainEntry、awake、精灵 Main 和 OnLoaded。
	// 主要使用方：queueBootstrap()、takeBootstrapTasks()、runBootstrapTasks()。
	pendingBootstrap []func()
	// 按精灵名称保存的自动碰撞层/掩码配置。
	// 主要使用方：buildSpriteCollisionInfos()、applyCollisionLayers()、spriteCollisionInfo()。
	sprCollisionInfos map[string]*spriteCollisionInfo
	// 当前舞台精灵的碰撞数据，供自动分配碰撞层时使用。
	// 主要使用方：buildSpriteCollisionData()、applyCollisionLayers()、resetCollisionLayerState()。
	sprCollisionData []*spriteCollisionData
	// 是否使用像素级碰撞检测。
	// 主要使用方：applyPhysicsSettings()、game_physics.go、PhysicsMgr.SetCollisionSystemType()。
	isCollisionByPixel bool
	// 是否根据精灵碰撞信息自动分配碰撞层。
	// 主要使用方：applyPhysicsSettings()、buildSpriteCollisionInfos()、applyCollisionLayers()。
	isAutoSetCollisionLayer bool

	// Go 侧输入状态和输入回放逻辑的管理器。
	// 主要使用方：OnEngineBeforeUpdate()、OnEngineUpdate()、runtime_events.go、runtime_loops.go。
	inputMgr inputManager
	// 保护输入会话的并发访问。
	// 主要使用方：attachPreparedInputSession()、currentInputSession()、abortInputSession()。
	inputSessionMu sync.RWMutex
	// 当前录制/回放输入会话。
	// 主要使用方：OnEngineBeforeUpdate()、OnEngineUpdate()、runtime_replay_session.go。
	inputSession *inputSession
	// 已结束输入会话的最终状态，供宿主查询。
	// 主要使用方：finishInputSession()、inputSessionStatus()、runtime_replay_session.go。
	inputTerminal InputSessionStatus
	// 当前 Game 是否已经占用宿主传入的输入会话。
	// 主要使用方：attachPreparedInputSession()、prepareInputSession()、abortInputSession()。
	inputClaimed bool
	// Go 音频门面，内部转发到 Godot AudioMgr。
	// 主要使用方：startLoad()、game_sound.go、OnEngineUpdate()、releaseGameAudio()。
	soundMgr audio.Manager
	// Go 侧舞台 Shape 集合，管理精灵、特殊形状和临时克隆形状。
	// 主要使用方：loadStage()、runtime_shapes.go、runtime_sync.go、collision_optimizer.go。
	shapeMgr shapeManager
	// Tilemap 的项目级加载、解析和当前地图状态。
	// 主要使用方：loadGameSprites()、setupAudioAndTilemap()、tilemap.go。
	tilemapMgr gameTilemapMgr

	// 精灵变换和删除操作的批量同步缓冲区，帧末统一提交给 Godot SpriteMgr。
	// 主要使用方：initGame()、runtime_sync.go、SpriteSyncBuffer。
	syncBuffer *engine.SpriteSyncBuffer
	// 画笔命令的批量同步缓冲区，统一提交给 Godot PenMgr。
	// 主要使用方：initGame()、runtime_pen_sync.go、PenSyncBuffer。
	penSyncBuffer *engine.PenSyncBuffer
	// 当前帧从 Godot PhysicsMgr 取得的触发事件临时切片。
	// 主要使用方：syncTriggerEvents()、OnEngineUpdate()、runtime_sync.go。
	triggerEvents []engine.TriggerEvent
	// 碰撞优化用的空间哈希，按精灵 AABB 缩小碰撞候选范围。
	// 主要使用方：collision_optimizer.go、碰撞查询和物理更新。
	spatialHash *collision.SpatialHash[*SpriteImpl]
}

func (p *Game) setDebugFlags(flags dbgFlags) {
	p.debugState.DebugInstr = flags&DbgFlagInstr != 0
	p.debugState.DebugEvent = flags&DbgFlagEvent != 0
	p.debugState.DebugPerf = flags&DbgFlagPerf != 0
	gco.SetPerfDebug(p.debugState.DebugPerf)
}

func (p *Game) newSpriteAndLoad(
	name string,
	tySpr reflect.Type,
	g reflect.Value,
	loadSprite spriteLoader,
) Sprite {
	spr := reflect.New(tySpr).Interface().(Sprite)
	if err := loadSprite(spr, name, g); err != nil {
		panic(err)
	}
	return spr
}

func (p *Game) getSpriteProto(tySpr reflect.Type, g reflect.Value, loadSprite spriteLoader) Sprite {
	name := tySpr.Name()
	spr, ok := p.sprs[name]
	if !ok {
		spr = p.newSpriteAndLoad(name, tySpr, g, loadSprite)
	}
	return spr
}

func (p *Game) getSpriteProtoByName(name string, g reflect.Value, loadSprite spriteLoader) Sprite {
	spr, ok := p.sprs[name]
	if !ok {
		tySpr, ok := p.typs[name]
		if !ok {
			spxlog.Panicf("Sprite %s is not defined", name)
		}
		spr = p.newSpriteAndLoad(name, tySpr, g, loadSprite)
	}
	return spr
}

func (p *Game) reset() {
	p.resetBootstrap()

	p.releaseGameAudio()
	p.EraseAll()

	p.scriptEvents.manager.Reset()
	p.shapeMgr.reset()

	p.debugState.DebugPanel = nil
	p.dialogState.AskPanel = nil

	p.Stop(AllOtherScripts)

	p.lifecycleState.RunOnce = sync.Once{}
	p.lifecycleState.OncePathFinder = sync.Once{}
	p.sprs = make(map[string]Sprite)

	engine.ResetFrameRuntime()
	engine.ResetInputState()
	p.abortInputSession("game reset")
	p.resetCollisionLayerState()
	costumeSizeCache.Clear()
	p.eventQueueState.EventQueueStats.Reset()
	p.events = nil

	itime.OnReload()
}

// initGame 准备一局游戏的 Go 侧运行时结构。
//
// 调用时机：XGot_Game_Main() -> engine.Main() 的 initialize 回调。
// Web 来源：ispx_start() 解释执行项目 main.go 后进入 XGot_Game_Main()。
// Native 来源：Godot 加载 Go 扩展后，项目 main.main() 进入 XGot_Game_Main()。
//
// 本函数只建立 Go 侧状态、类型表和批量同步缓冲区；它不会读取项目的
// assets/project 配置，也不会执行精灵 awake/Main。项目资源和 Godot 精灵
// 代理节点由后续 Game.OnEngineStart() -> loadGame() 进入 bootstrap 时创建。
func (p *Game) initGame(sprites []Sprite) *Game {
	// 清理上一局可能残留的帧级引擎状态和服装尺寸缓存。
	engine.ResetFrameRuntime()
	costumeSizeCache.Clear()

	// 接收 Start 阶段准备好的录制/回放输入会话；失败属于启动错误。
	if err := p.attachPreparedInputSession(); err != nil {
		engine.Panic(err)
	}

	// 初始化 Go 侧的舞台形状管理器；此时还没有根据 project.json 加载具体形状。
	p.initShapeMgr()

	// 使用全局调试配置初始化本局的指令、事件和性能调试开关。
	p.setDebugFlags(dbgFlags(defaultDebugFlags.Load()))

	// 设置脚本事件队列的默认策略和统计信息。
	p.initEventQueueState()

	// 把 Game 自身绑定到脚本事件注册表，后续 awake、按键、碰撞等事件
	// 才能找到对应的 Go 处理函数。
	p.bindScriptEvents()

	// 建立本局运行时对象表。sprs/sounds 会在 bootstrap 加载项目时填充。
	p.sprs = make(map[string]Sprite)
	p.sounds = make(map[string]sound)

	// 建立“精灵名称 -> Go 反射类型”的表。
	// 后续 loadStage() 根据 project.json 中的精灵名称，从这里创建具体 Go 精灵对象。
	p.typs = make(map[string]reflect.Type)

	// 创建精灵变换批量同步缓冲区，避免每个属性变化都立即跨 Go/JS/WASM 调用。
	p.syncBuffer = engine.NewSpriteSyncBuffer(initialSpriteSyncBufferSize)

	// 创建画笔命令批量同步缓冲区；画笔命令后续由 PenMgr 统一提交到 Godot。
	p.penSyncBuffer = engine.NewPenSyncBuffer(initialPenSyncBufferSize)

	// sprites 来自项目生成的 Go main 初始化参数。这里只登记类型，暂不加载
	// sprite.json、舞台实例或 Godot AnimatedSprite2D 节点。
	for _, spr := range sprites {
		tySpr := reflect.TypeOf(spr).Elem()
		p.typs[tySpr.Name()] = tySpr
	}
	return p
}

func (p *Game) baseGame() *Game {
	return p
}

func (p *Game) initShapeMgr() {
	p.shapeMgr.init()
}

func currentGame() *Game {
	game, _ := engine.GetGame().(*Game)
	return game
}

func isDebugInstrEnabled() bool {
	if game := currentGame(); game != nil {
		return game.debugState.DebugInstr
	}
	return dbgFlags(defaultDebugFlags.Load())&DbgFlagInstr != 0
}

func isDebugEventEnabled() bool {
	if game := currentGame(); game != nil {
		return game.debugState.DebugEvent
	}
	return dbgFlags(defaultDebugFlags.Load())&DbgFlagEvent != 0
}
