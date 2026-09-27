# SPX 从引擎启动到每帧 Update 的完整调用流程

本文从 Godot、C++ SPX 模块、Go Godot 代理层、Go 游戏运行时和协程调度器五个层次，梳理一局游戏从绑定、启动、Bootstrap 到每帧更新的完整调用链。

## 1. 先区分五个层次

```text
Godot 主循环
    |
    v
godot_modules/spx（C++ SPX 模块）
    |
    | Native C ABI / Web JS-WASM 回调
    v
internal/gdengine（Go 的 Godot 代理层）
    |
    v
internal/engine（Go 核心引擎调度层）
    |
    v
根包 spx.Game（Go 游戏运行时与业务对象）
    |
    v
internal/coroutine（脚本协程调度器）
```

各层主要职责：

| 层次 | 主要职责 |
| --- | --- |
| Godot 主循环 | 提供启动、逻辑帧、物理帧和销毁时机 |
| `godot_modules/spx` | 管理 Godot 节点、资源、渲染、物理和 C++ Manager |
| `internal/gdengine` | 将 Native/Web 回调统一成 Go 接口，维护 Go 侧 Godot 代理对象 |
| `internal/engine` | 管理一局游戏、帧阶段、输入缓存、时间、协程入口和后端绑定 |
| 根包 `spx` | 管理 `Game`、`SpriteImpl`、事件注册、Bootstrap 和数据同步 |
| `internal/coroutine` | 串行运行脚本片段，处理 `Wait`、`Yield`、Join 和主线程任务 |

需要特别区分三类 `OnStart`：

```text
C++ SpxManager::on_start()
    C++ Manager 的生命周期

Go IManager.OnStart()
    Go Godot 代理 Manager 的生命周期

用户脚本 OnStart(func)
    Bootstrap 完成后统一派发的脚本事件
```

## 2. Go 游戏运行时绑定

入口：`internal/engine/engine.go:102` 的 `Main()`。

```text
internal/engine.Main(game, owner, initialize)
    // 建立并持有一局游戏的完整运行时会话
    |
    +-- bindGameAtPhase(game, owner, gameStarting)
    |     // 把 Game 绑定为 activeGame
    |     // 初始阶段为 gameStarting，防止同时运行多个 Game
    |
    +-- initialize()
    |     // 执行项目生成代码提供的初始化函数
    |     // 通常包括运行时对象和类型表的准备
    |
    +-- enginewrap.Init(WaitMainThread)
    |     // 初始化 Go Manager 包装层
    |     // 将需要 Godot 主线程执行的调用统一接到 WaitMainThread
    |
    +-- 构造 gdx.CoreCallbackInfo
    |     |
    |     +-- OnEngineStart     -> internal/engine.onStart
    |     +-- OnEngineUpdate    -> internal/engine.onUpdate
    |     +-- OnEngineDestroy   -> internal/engine.onDestroy
    |     +-- OnEngineReset     -> internal/engine.onReset
    |     +-- OnMousePressed    -> internal/engine.onMousePressed
    |     +-- OnKeyPressed      -> internal/engine.onKeyPressed
    |     `-- 其他输入、暂停和销毁回调
    |           // 此时只建立函数表，还没有执行这些回调
    |
    +-- gde.PrepareLink(callbacks)
    |     // 建立 Go 与 Native/Web Godot 后端的绑定
    |     |
    |     +-- facade.LinkFFI()
    |     |     // Native：解析 GDExtension 导出函数
    |     |     // Web：解析 JS/WASM 桥接函数
    |     |
    |     +-- coreCallbacks = callbacks
    |     |     // 保存 internal/engine 提供的核心回调
    |     |
    |     +-- facade.RegisterCallbacks(bindCallbacks())
    |     |     // 向平台后端注册 internal/gdengine 的公共回调表
    |     |     // 包括 onEngineStart、onEngineUpdate、onSpriteReady 等
    |     |
    |     +-- engineimpl.CreateMgrs()
    |     |     // 创建 Go 侧 Godot Manager 代理
    |     |
    |     +-- engineimpl.BindMgr(mgrs)
    |     |     // 写入 pkg/spx/pkg/engine 中的全局 Manager 接口变量
    |     |
    |     `-- activeLink = session
    |           // 当前会话获得后端清理权
    |
    `-- binding.link.Run(finishStart)
          // 启动平台会话
          // Native/pure 通常立即完成准备；Web 会话可能持续到 Unlink
```

注意事项：

1. `PrepareLink()` 完成的是“连接和代理初始化”，不是项目资源加载。
2. Go Manager 代理和 C++ Manager 是两组对象。前者通过接口/FFI 调用后者。
3. Web 解释器模式没有真实的 C++ 启动回调，会在 `LinkSession.Run()` 中补发 `OnEngineStart`。

## 3. Godot 模块和 C++ SpxEngine 初始化

```text
Godot 模块初始化
    |
    +-- Spx::register_types()
    |     // 向 ClassDB 注册 SpxSprite、SpxUi、SpxDrawTiles 等类型
    |
    +-- Spx::register_extension_functions()
    |     // 注册 Godot 与 Go/Web 之间的 ABI 函数
    |
    +-- Spx::register_main_loop_callbacks()
    |     // 向 MainLoopPhaseCallbackBus 注册启动、更新、物理和销毁入口
    |
    `-- spx_global_register_callbacks(...)
          // 接收 Go 导出的函数地址
          |
          `-- SpxEngine::register_callbacks(callbacks)
                // 创建进程内唯一 SpxEngine
                |
                `-- SpxEngine::_initialize_managers()
                      // 按依赖顺序创建所有 C++ Manager
                      +-- SpxInputMgr
                      +-- SpxAudioMgr
                      +-- SpxPhysicsMgr
                      +-- SpxSpriteMgr
                      +-- SpxUiMgr
                      +-- SpxSceneMgr
                      +-- SpxCameraMgr
                      +-- SpxPlatformMgr
                      +-- SpxResMgr
                      +-- SpxDebugMgr
                      +-- SpxNavigationMgr
                      +-- SpxPenMgr
                      +-- SpxTilemapMgr
                      `-- SpxTilemapparserMgr
```

相关代码：

- `godot_modules/spx/spx.cpp`
- `godot_modules/spx/spx_engine.cpp:79`
- `godot_modules/spx/spx_engine.cpp:504`
- `internal/gdengine/linker.go:101`

## 4. Godot 启动回调进入 Go

Godot `SceneTree` 开始运行后，通过主循环阶段总线进入 SPX：

```text
Godot SceneTree start
    |
    `-- MainLoopPhaseCallbackBus
          |
          `-- _spx_main_loop_start(MainLoop*)
                // Godot 主循环的 SPX 启动入口
                |
                `-- Spx::on_start(MainLoop*)
                      // godot_modules/spx/spx.cpp:162
                      |
                      +-- 校验 SceneTree 和根 Window
                      |
                      +-- memnew(SpxEngineNode)
                      |     // 创建 SPX 场景树锚点
                      |
                      +-- root->add_child(new_node)
                      |     // 挂入 Godot 场景树，由 SceneTree 管理节点生命周期
                      |
                      +-- SpxEngine::set_root_node(tree, new_node)
                      |     // 保存 SceneTree、Window 和 SPX 根节点引用
                      |
                      `-- SpxEngine::on_awake()
                            // godot_modules/spx/spx_engine.cpp:160
                            |
                            +-- _notify_managers(SpxManager::on_awake)
                            |     // 创建/连接 Manager 所需的 Godot 节点和资源
                            |
                            +-- _notify_managers(SpxManager::on_start)
                            |     // 完成 C++ Manager 启动生命周期
                            |
                            `-- callbacks.func_on_engine_start()
                                  // 跨 C++ -> Go 边界
                                  |
                                  `-- func_on_engine_start()
                                        // Native C ABI 入口
                                        |
                                        `-- callbacks.OnEngineStart()
                                              |
                                              `-- gdengine.onEngineStart()
                                                    // internal/gdengine/callbacks.go:91
                                                    |
                                                    +-- mgr.OnStart()
                                                    |     // 启动 Go Manager 代理
                                                    |
                                                    `-- coreCallbacks.OnEngineStart()
                                                          |
                                                          `-- internal/engine.onStart()
                                                                // internal/engine/engine.go:145
                                                                |
                                                                +-- ResetInputState()
                                                                |     // 清空进程级键盘、鼠标状态
                                                                |
                                                                +-- resetTriggerEvents()
                                                                |     // 清空上一局遗留触发事件
                                                                |
                                                                +-- itime.Start(...)
                                                                |     // 初始化 SPX 逻辑时间
                                                                |
                                                                `-- Game.OnEngineStart()
                                                                      // 进入具体游戏的异步启动流程
```

启动回调本身不直接同步执行完整项目加载。`Game.OnEngineStart()` 使用 `engine.Go()` 创建受管协程，避免长期占用 Godot 启动回调栈。

## 5. Game.OnEngineStart 和 Bootstrap

入口：`runtime_engine.go:36`。

### 5.1 第一层启动协程 T0

```text
Game.OnEngineStart()
    |
    +-- lifecycleState.RunOnce.Do(...)
    |     // 同一局只允许进入一次
    |
    +-- generation := bootstrapGeneration()
    |     // 捕获当前生命周期代际，reset 后旧任务自动失效
    |
    `-- engine.Go(Game, fn)
          // gco.Create 创建启动协程 T0
          // 创建时先登记 threadRunnable，再启动底层 Go goroutine
          |
          `-- T0
                |
                +-- isCurrentBootstrap(generation)
                |     // 防止旧一局的异步任务修改新一局
                |
                +-- queueBootstrap(MainEntry)
                |     // 只将 func() 写入 pendingBootstrap
                |     // pendingBootstrap 不是协程队列
                |
                +-- loadGame("assets", generation)
                |     // 加载项目数据并建立 Go/Godot 运行对象
                |
                +-- markGameStarted(generation)
                |     // lifecycleState.IsRunned = true
                |     // 表示正常帧逻辑已经可以进入
                |
                `-- startBootstrap(generation)
                      // 创建第二层 Bootstrap 协程 T1
```

`engine.Go()` 最终调用 `gco.Create()`。创建顺序是：

```text
创建 Thread 对象
    -> registerThread()
    -> 标记 threadRunnable
    -> go runThread(...)
    -> runThread 竞争 runMu
    -> 获得 runMu 后执行用户函数
```

因此，`runnable` 表示“调度器必须等待其运行片段交还执行权”，不保证该 goroutine 已经获得 CPU 或 `runMu`。

### 5.2 loadGame 内部调用

入口：`game_build.go:38`。

```text
loadGame(resource, generation)
    |
    +-- coreproject.OpenBuilderResources()
    |     // 打开项目配置、字体计划和 assets 文件系统
    |
    +-- engine.SetAssetDir()
    |     // 设置后续资源路径基准
    |
    +-- applyRuntimeFontPlan()
    |     // 通过 ResMgr 应用项目字体
    |
    +-- parseCommandLineFlags()
    |     // 解析运行参数
    |
    +-- applyRuntimeConfig()
    |     // 应用项目级运行配置
    |
    +-- setupGameSystems()
    |     // 设置物理、音频、寻路和层排序
    |
    +-- loadGameSprites()
    |     // 反射遍历 Gamer 的精灵字段
    |     |
    |     +-- startLoad()
    |     |     // 初始化声音、输入、事件通道和项目 FS
    |     |
    |     +-- coreproject.WalkFields()
    |     |     // 找到声明的精灵业务对象
    |     |
    |     `-- loadSprite()/SpriteImpl.init()
    |           // 初始化 Go SpriteImpl
    |           // 创建对应 engine.Sprite Godot 代理
    |           // 加入 Game.sprs 和 shapeManager
    |
    +-- loadStage()
    |     // 建立背景、相机、TileMap、窗口和精灵 Z 序
    |     |
    |     `-- runSpriteCallbacks()
    |           // 将不能立即运行的生命周期函数加入 Bootstrap 队列
    |
    +-- initEventLoop()
    |     // 创建键盘、鼠标等运行时事件循环协程
    |
    `-- PlatformMgr.SetWindowTitle()
          // 更新 Godot 窗口标题
```

### 5.3 pendingBootstrap 中的任务顺序

`runSpriteCallbacks()` 位于 `game_load.go:225`：

```text
pendingBootstrap
    |
    +-- 项目 MainEntry
    |     // 在 OnEngineStart 中最先登记
    |
    +-- setupCollisionData(inits)
    |     // 初始化所有精灵的碰撞数据
    |
    +-- sprite.awake（逐个精灵）
    |     // 执行精灵内部 Awake 生命周期
    |
    +-- runSpriteMainsUntilYield(inits)
    |     // 按加载/Z 序执行每个精灵 Main 的首个片段
    |
    `-- Game.OnLoaded
          // 项目资源和初始对象准备完成后的业务回调
```

### 5.4 Bootstrap 协程 T1

```text
startBootstrap(generation)
    |
    `-- engine.Go(Game, fn)
          // 创建 Bootstrap 协程 T1
          |
          `-- T1
                |
                +-- currentGame() == Game
                |     // Game 必须仍然有效
                |
                +-- claimBootstrap(generation)
                |     // 同一代际只能有一个 Bootstrap 执行者
                |
                +-- runBootstrapTasks(generation)
                |     // 逐批取出 pendingBootstrap 并直接调用 task()
                |     // task 执行期间新登记的任务在下一轮继续取出
                |
                `-- completeBootstrap(generation)
                      // lifecycleState.BootstrapDone = true
```

`runBootstrapTasks()` 本身在 T1 中执行。T1 何时真正运行取决于 Go 调度和 `runMu`：

```text
T1 已登记为 runnable，但还没有获得 runMu
    |
    +-- T0 可能仍在执行并持有 runMu
    |
    `-- gco.Update() 若已经进入
          // 看到 T1 仍是 runnable
          // 在 schedulerCond 上等待 T1 Yield、阻塞或结束
```

`gco.Update()` 不负责调用 T1 的函数。底层 Go goroutine 自己进入 `runThread()` 并竞争 `runMu`；`gco.Update()` 负责观察和等待调度状态。

### 5.5 Main 子协程 T2

```text
T1 -> runSpriteMainsUntilYield()
        |
        `-- runMainUntilYield(owner, Main)
              |
              +-- gco.Create(...)
              |     // 创建承载 Main 的子协程 T2
              |
              `-- gco.JoinYieldedOrDone(T2)
                    // T1 进入 blocked，并登记到 T2.yieldWaiters
                    // 只等待 T2 第一次 Yield 或直接结束
```

两种结果：

```text
T2 的 Main 直接返回
    -> finishThread(T2)
    -> 唤醒 T1
    -> T1 执行下一个 Bootstrap 任务

T2 的 Main 调用 Wait/WaitNextFrame/Yield
    -> T2 进入 blocked
    -> 关闭 yieldedOrDone
    -> 唤醒 T1
    -> T1 执行下一个 Bootstrap 任务
    -> T2 后续生命周期由未来 gco.Update 恢复
```

注意：Bootstrap 只等待每个 Main 的首段，不等待所有 Main 完整结束。

## 6. Godot 每帧 Update 的跨层入口

```text
Godot 每个逻辑/渲染帧
    |
    `-- MainLoopPhaseCallbackBus
          |
          `-- _spx_main_loop_update(delta)
                // godot_modules/spx/spx.cpp:76
                |
                `-- Spx::on_update(delta)
                      // 在 Godot 主线程执行
                      |
                      +-- pending_controls.take(RESTART)
                      |     // 重启优先；命中后本帧不再普通更新
                      |
                      +-- pending_controls.take(RESET)
                      |     // 重置优先；命中后本帧不再普通更新
                      |
                      +-- pending_controls.take(PAUSE)
                      +-- pending_controls.take(RESUME)
                      +-- pending_controls.take(NEXT_FRAME)
                      |     // 统一消费其他线程提交的主线程控制命令
                      |
                      `-- SpxEngine::on_update(delta)
                            // godot_modules/spx/spx_engine.cpp:194
                            |
                            +-- 检查 has_exit/is_spx_paused
                            |     // 退出或暂停时可能跳过整帧
                            |
                            +-- _notify_managers(SpxManager::on_update, delta)
                            |     // 先更新全部 C++ Manager
                            |
                            +-- callbacks.func_on_engine_update(delta)
                            |     // 跨 C++ -> Go 边界
                            |
                            +-- pen->flush_all()
                            |     // Go 回调返回后统一提交 C++ 画笔表面更新
                            |
                            `-- 必要时同步 SceneTree pause 状态
```

## 7. Go Godot 代理层的每帧更新

Native 调用链：

```text
callbacks.func_on_engine_update(delta)
    |
    `-- func_on_engine_update(delta)
          // internal/gdengine/binding/native/gdextension_interface.go:658
          |
          `-- callbacks.OnEngineUpdate(delta)
                |
                `-- internal/gdengine.onEngineUpdate(delta)
                      // internal/gdengine/callbacks.go:101
                      |
                      +-- EffectiveLogicalDeltaTime(delta)
                      |     // 输入回放期间改用固定逻辑步长
                      |
                      +-- mgr.OnUpdate(delta)
                      |     // 遍历 Go Manager 代理生命周期钩子
                      |     // 默认实现可能为空，扩展实现可在此更新
                      |
                      +-- AdvanceTimeSinceGameStart(delta)
                      |     // 推进 Go Godot 代理层的运行时间
                      |
                      +-- Sprites()
                      |     // 从 internal/engine.state.sprites 复制稳定快照
                      |
                      +-- sprite.OnUpdate(delta)
                      |     // 更新 Go 侧 Godot 精灵代理
                      |     // 不是业务层 SpriteImpl.Main/OnStart
                      |
                      +-- coreCallbacks.OnEngineUpdate(delta)
                      |     |
                      |     `-- internal/engine.onUpdate(delta)
                      |           // 一帧 Go 游戏逻辑的总调度入口
                      |
                      `-- InternalUpdateEngine(delta)
                            // coreCallbacks 返回后执行
                            +-- updateTimers(delta)
                            `-- updateTweens(delta)
```

注意：`InternalUpdateEngine(delta)` 位于 `internal/engine.onUpdate()` 完整返回之后，用于更新 Go 引擎代理层的延迟调用和 Tween。

## 8. internal/engine.onUpdate 完整树

入口：`internal/engine/engine.go:167`。

```text
internal/engine.onUpdate(delta)
    // 一帧 Go 游戏逻辑的总调度入口
    |
    +-- defer CheckPanic()
    |     // 将本帧未处理 panic 交给引擎错误机制
    |
    +-- updateMu.Lock()
    |     // 防止同一局 Game 被重入执行两个 Update
    |
    +-- updateBusy = true
    |     // 销毁、重置流程据此判断当前是否正在更新
    |
    +-- binding := runningBinding()
    |     // 只接受仍处于 gameRunning 的当前绑定
    |
    +-- profiler.BeginSample()
    |     // 开始一帧性能统计
    |
    +-- cacheTriggerEvents()
    |     // 物理触发 pending -> ready
    |     // 只交换帧快照，不运行用户事件函数
    |
    +-- cacheKeyEvents()
    |     // 键盘边沿 pending -> ready
    |
    +-- cacheMouseEvents()
    |     // 鼠标边沿 pending -> ready
    |
    +-- Game.OnEngineBeforeUpdate(delta)
    |     // 时间推进前的输入/条件采样阶段
    |     |
    |     +-- pendingConditions = nil
    |     |     // 丢弃上一帧未派发条件快照
    |     |
    |     +-- inputMgr.prepareInputSessionTick(...)
    |     |     // 录制模式采样 ready 输入
    |     |     // 回放模式解析记录中的确定性输入帧
    |     |
    |     `-- scriptEvents.sampleConditions()
    |           // 仅在 StartDispatched 后评估 OnCond 上升沿
    |           // 只保存命中 sink，不创建用户处理器协程
    |
    +-- 再次检查 binding.isCurrent(gameRunning)
    |     // BeforeUpdate 可能触发 reset/destroy
    |
    +-- itime.Update(delta, fps)
    |     // 推进 SPX 逻辑帧、逻辑时间和 DeltaTime
    |
    +-- Game.OnEngineUpdate(delta)
    |     // 用户脚本协程实际运行前的登记、同步和物理回读阶段
    |     |
    |     +-- 检查 IsRunned
    |     |     // loadGame 尚未完成时直接返回
    |     |
    |     +-- scriptEvents.dispatchConditions()
    |     |     // 使用 BeforeUpdate 保存的条件快照
    |     |     // 为命中处理器创建协程，主体仍受 gco 调度
    |     |
    |     +-- inputMgr.dispatchInputSessionTick()
    |     |     // 派发录制/回放输入产生的高层事件
    |     |
    |     +-- soundMgr.Update()
    |     |     // 更新 Go 游戏层声音状态
    |     |
    |     +-- runFrameScripts()
    |     |     // 在 OnStart 门控和普通 AtFrame 回调之间选择
    |     |     |
    |     |     +-- BootstrapDone && !StartDispatched
    |     |     |     |
    |     |     |     `-- dispatchStartEventIfNeeded()
    |     |     |           |
    |     |     |           +-- scheduleStartEvent()
    |     |     |           |     // 检查 BootstrapDone、StartDispatched、startScheduled
    |     |     |           |     // 保证一局只安排一次 eventStart
    |     |     |           |
    |     |     |           `-- handleEvent(eventStart)
    |     |     |                 |
    |     |     |                 +-- gco.Create(startEventDispatcher)
    |     |     |                 |     // 创建 OnStart 派发器协程
    |     |     |                 |
    |     |     |                 `-- 派发器 runStartPhase()
    |     |     |                       |
    |     |     |                       +-- takeStartSinks(generation)
    |     |     |                       |     // SnapshotStartOnce 关闭后续全局 OnStart 注册
    |     |     |                       |
    |     |     |                       +-- doWhenStart(sinks)
    |     |     |                       |     |
    |     |     |                       |     `-- StartBatch(BatchWaitFirstSlice)
    |     |     |                       |           // 按 Scratch 目标顺序创建 OnStart 协程
    |     |     |                       |           // 等待每个处理器首段 Yield 或结束
    |     |     |                       |
    |     |     |                       `-- markStartDispatched()
    |     |     |                             // lifecycleState.StartDispatched = true
    |     |     |
    |     |     `-- 其他情况
    |     |           |
    |     |           `-- engine.RunFrameCallbacks()
    |     |                 |
    |     |                 +-- frameCallbackQueue.takeDue(CurrentFrame)
    |     |                 |     // 取出目标帧 <= 当前帧的 AtFrame 回调
    |     |                 |     // 未到期回调继续留在队列
    |     |                 |
    |     |                 +-- 按目标帧稳定排序
    |     |                 |
    |     |                 `-- executeFrameCallbacks()
    |     |                       // 从引擎线程调用时先创建分发协程
    |     |                       // 再为每个回调创建独立协程
    |     |
    |     +-- updateSpriteProxies()
    |     |     // gco.Update 前的第一轮 Go -> Godot 同步
    |     |     |
    |     |     +-- camera.onUpdate()
    |     |     |     // 更新相机代理状态
    |     |     |
    |     |     +-- shapeMgr.getTempShapes()
    |     |     |     // 获取稳定 Shape 快照，避免遍历中集合变化
    |     |     |
    |     |     +-- shapeMgr.flushActivate()
    |     |     |     // 推进 Shape 激活/可见性状态
    |     |     |
    |     |     `-- flushSpriteProxyChanges(activeShapes)
    |     |           |
    |     |           +-- syncBuffer.Clear()
    |     |           |     // 清理可复用批量缓冲区
    |     |           |
    |     |           +-- shapeMgr.collectProxyUpdates()
    |     |           |     // 收集位置、旋转、缩放、渲染偏移、可见性等脏数据
    |     |           |
    |     |           +-- shapeMgr.flushDestroy()
    |     |           |     // 将待销毁对象 ID 追加到删除区
    |     |           |
    |     |           `-- flushSyncBuffer()
    |     |                 |
    |     |                 +-- SpriteSyncBuffer.Serialize()
    |     |                 |     // 编码 updateCount/deleteCount/更新项/删除 ID
    |     |                 |
    |     |                 `-- engine.SyncBatchUpdateSprites()
    |     |                       // 经 Go Manager 和 FFI 批量写入 Godot SpxSpriteMgr
    |     |
    |     `-- pullPhysicsPositions()
    |           // Godot 物理 -> Go 的位置回读
    |           |
    |           `-- SyncBatchPositions()
    |                 |
    |                 +-- 筛选 shouldPullPhysicsPosition 的 SpriteImpl
    |                 +-- syncBuffer.GetPositions(spriteIDs)
    |                 |     // 最终进入 Godot BatchRetrievePositions
    |                 `-- SpriteImpl.applyPhysicsPosition(x, y)
    |                       // 回写业务精灵位置状态
    |
    +-- 再次检查 binding.isCurrent(gameRunning)
    |     // GameUpdate 期间也可能 reset/destroy
    |
    +-- gco.Update()
    |     // 协程等待任务处理和本帧脚本执行阶段
    |     |
    |     +-- beginUpdate()
    |     |     // 固定本轮 frame、levelTime、工作预算和 watchdog
    |     |
    |     +-- nextUpdateAction()
    |     |     |
    |     |     +-- 队首是 waitTypeMainThread
    |     |     |     // 即使还有 runnable 脚本也优先执行，解除脚本等待主线程的依赖
    |     |     |
    |     |     +-- 存在 runnable Thread
    |     |     |     // schedulerCond.Wait，等待其 Yield、阻塞或结束
    |     |     |
    |     |     `-- 没有 runnable 且没有当前任务
    |     |           // 当前 Update 可以完成，或检查同帧额外轮次
    |     |
    |     +-- processWaitJob()
    |     |     +-- waitTypeFrame      // 至少下一逻辑帧恢复
    |     |     +-- waitTypeTime       // 时间到且至少跨帧恢复
    |     |     +-- waitTypeYield      // 当前 Update 中恢复
    |     |     +-- waitTypeLoop       // 可能驱动同帧下一脚本轮次
    |     |     +-- waitTypeNextRound  // 可跟随下一轮但不主动驱动
    |     |     `-- waitTypeMainThread // 在 Godot/引擎线程执行同步任务
    |     |
    |     +-- 等待 runnable 脚本交还执行权
    |     |     // 包括 OnStart、输入、条件、AtFrame、Main 等处理器
    |     |     // 等待的是当前可运行片段，不是所有协程完整生命周期
    |     |
    |     `-- promoteDeferredJobs()
    |           // 将未到期任务稳定排序后留给后续帧
    |
    +-- 再次检查 binding.isCurrent(gameRunning)
    |
    +-- Game.OnEngineRender(delta)
    |     // 名称为 Render，实际是 Godot 绘制前的数据准备
    |     |
    |     +-- defer flushPenCommands()
    |     |     // 提交本帧 Go 侧画笔命令
    |     |
    |     +-- shapeMgr.takeCloneProxyPublications()
    |     |     // 消费已经完成首段初始化的克隆公开通知
    |     |
    |     +-- syncPostCoroutineVisuals()
    |     |     // gco.Update 后的第二轮 Go -> Godot 视觉同步
    |     |     |
    |     |     +-- camera.onUpdate()
    |     |     +-- flushSpriteProxyChanges()
    |     |     |     // 补交协程刚刚产生的位置、服装和可见性变化
    |     |     `-- shapeMgr.flushBubbleVisuals()
    |     |           // 使用最终精灵边界重新布局气泡
    |     |
    |     `-- processPhysicsTriggers()
    |           |
    |           +-- engine.GetTriggerEvents()
    |           |     // 取走 ready 触发事件
    |           |
    |           +-- 过滤隐藏、已销毁或无效对象
    |           |
    |           `-- fireTouchStart()
    |                 // 创建 OnTouchStart 事件协程
    |                 // 此时本帧 gco.Update 已结束，通常下一帧才实际执行
    |
    +-- FlushCaptures()
    |     // 渲染准备完成后提交截图请求
    |
    `-- Game.OnEngineFrameEnd()
          // 完成输入录制/回放的帧末状态转换
```

## 9. runFrameScripts 的准确含义

实现位于 `runtime_engine.go:182`：

```go
func (p *Game) runFrameScripts() {
	if p.lifecycleState.BootstrapDone.Load() &&
		!p.lifecycleState.StartDispatched.Load() {
		p.dispatchStartEventIfNeeded()
		return
	}
	engine.RunFrameCallbacks()
}
```

它实际负责“推进本帧的脚本入口阶段”：

```text
BootstrapDone && !StartDispatched
    -> 本帧只派发一次 OnStart
    -> return，不同时取出 AtFrame 回调

其他情况
    -> RunFrameCallbacks()
    -> 取出到期的 AtFrame 回调并创建协程
```

一个容易忽略的细节：

```text
BootstrapDone == false
    -> 条件不成立
    -> 仍然会进入 RunFrameCallbacks()
```

因此，绝对帧号已经到期的 `AtFrame` 回调可能在 Bootstrap 完成前被取出。只有“首次派发 OnStart 的那一帧”会因为 `return` 暂停普通帧回调一次。

`runFrameScripts` 可理解为“运行/派发本帧脚本阶段”，如果强调它只负责登记入口，`dispatchFrameScriptPhase` 会是更精确的名称。

## 10. RunFrameCallbacks 执行什么

```text
用户调用 AtFrame(frame, fn)
    |
    `-- engine.ScheduleFrame(frame, fn)
          |
          +-- 捕获注册者 owner 和 source Thread
          |
          +-- frame <= CurrentFrame
          |     // 已到期，立即进入 executeFrameCallback
          |
          `-- frame > CurrentFrame
                // 加入 frameRuntime.callbacks
```

每帧：

```text
engine.RunFrameCallbacks()
    |
    +-- callbacks.takeDue(itime.Frame())
    |     // 取出 frame <= 当前帧的回调
    |     // 未来回调留在队列
    |
    +-- SortStableFunc(frame)
    |     // 按目标帧排序，同帧保持注册顺序
    |
    `-- executeFrameCallbacks()
          |
          +-- 当前在引擎线程而非受管协程
          |     // 先创建一个分发协程，不在引擎线程同步 Join
          |
          `-- executeFrameCallback(callback)
                |
                +-- source Thread 已停止
                |     // 丢弃该回调
                |
                +-- gco.Create(callback.owner, callback.fn)
                |     // 每个回调获得独立受管协程
                |
                `-- 如果调用者本身是受管协程
                      // JoinYieldedOrDone，保持立即语义直到回调首段让出或结束
```

`AtFrame` 和 `WaitNextFrame` 不是同一套队列：

```text
AtFrame
    -> frameRuntime.callbacks
    -> RunFrameCallbacks
    -> 为到期回调创建新协程

WaitNextFrame
    -> coroutine.currentJobs/deferredJobs
    -> gco.Update
    -> 恢复原协程
```

## 11. 输入事件旁路

键盘和鼠标事件通常在帧回调之外先到达：

```text
Godot InputEvent
    |
    `-- C++ SpxInputMgr/InputProxy
          |
          `-- func_on_key_pressed / func_on_mouse_pressed
                // 跨 C++ -> Go 边界
                |
                `-- gdengine.onKeyPressed/onMousePressed
                      |
                      `-- coreCallbacks.OnKeyPressed/OnMousePressed
                            |
                            `-- internal/engine.onKeyPressed/onMousePressed
                                  |
                                  +-- 更新 pressed/button 状态
                                  `-- 写入 pending 边沿队列
```

下一帧：

```text
internal/engine.onUpdate()
    |
    +-- cacheKeyEvents/cacheMouseEvents
    |     // pending -> ready，形成稳定帧快照
    |
    +-- inputEventLoop 或输入会话派发
    |     // 将 ready 底层输入转为 OnKey、OnClick 等高层事件
    |
    `-- 创建用户事件处理器协程
          // 由 gco.Update 取得实际执行权
```

注意：鼠标按住状态可能在底层回调时立即更新，但边沿事件仍在帧边界缓存和消费。

## 12. 物理触发事件旁路

```text
Godot 物理系统确认 Trigger Enter
    |
    `-- C++ SpxSprite/SpxPhysicsMgr 回调
          |
          `-- func_on_trigger_enter(srcID, dstID)
                |
                `-- gdengine.onTriggerEnter(srcID, dstID)
                      |
                      +-- GetSprite(srcID)
                      +-- GetSprite(dstID)
                      `-- sprite.OnTriggerEnter(other)
                            |
                            `-- enqueueTriggerEvent(src, dst)
                                  // 写入 triggerEvents.pending
```

下一帧：

```text
internal/engine.onUpdate()
    |
    +-- cacheTriggerEvents()
    |     // pending -> ready
    |
    +-- Game.OnEngineUpdate()
    +-- gco.Update()
    |
    `-- Game.OnEngineRender()
          |
          `-- processPhysicsTriggers()
                |
                +-- GetTriggerEvents()
                +-- 校验精灵有效性
                `-- fireTouchStart()
                      // 创建 OnTouchStart 协程
```

因为 `processPhysicsTriggers()` 位于本帧 `gco.Update()` 之后，所以这里创建的处理器一般到下一帧 `gco.Update()` 才真正运行。

## 13. 一帧中的两轮视觉同步

```text
Game.OnEngineUpdate
    |
    `-- updateSpriteProxies()
          // 第一轮同步
          // 提交本帧开始前已经存在的 Go 状态变化

gco.Update()
    // 用户脚本可能修改位置、服装、可见性、气泡等

Game.OnEngineRender
    |
    `-- syncPostCoroutineVisuals()
          // 第二轮同步
          // 补交协程在本帧刚产生的视觉变化
```

如果只有第一轮同步，`gco.Update()` 中发生的视觉变化就可能晚一帧才传到 Godot。

## 14. gco.Update 的核心边界

`gco.Update()` 的职责不是简单地“启动所有协程”，而是：

1. 处理本帧到期的 `WaitJob`；
2. 恢复对应的已挂起协程；
3. 观察已经登记为 `runnable` 的新协程；
4. 等待这些 runnable 协程执行到下一次 Yield、阻塞或结束；
5. 将未到期任务保留到未来帧。

```text
Create(Thread)
    -> 先标记 runnable
    -> 再启动 Go goroutine
    -> goroutine 竞争 runMu

gco.Update()
    -> 如果看到 runnable 非空
    -> schedulerCond.Wait()
    -> 等待 Thread 发布 blocked/finished 状态
```

因此可能出现：

```text
Thread 已经是 runnable
但底层 goroutine 尚未获得 CPU 或 runMu
gco.Update 已进入等待
```

这不是 `gco.Update()` 在调用 Thread，而是调度器等待该 Thread 的当前执行片段完成交接。

返回条件可以概括为：

```text
当前没有未取消的 runnable Thread
并且没有本轮可处理的 WaitJob
```

已经因 `WaitNextFrame`、未来时间或外部条件进入 blocked 的协程，不会阻止本轮 `gco.Update()` 返回。

## 15. 三个关键生命周期状态

```text
初始状态
    IsRunned      = false
    BootstrapDone = false
    StartDispatched = false

loadGame 完成，markGameStarted()
    IsRunned = true
    // 资源、场景和初始代理已建立
    // Bootstrap 任务可能仍未完成

runBootstrapTasks 完成，completeBootstrap()
    BootstrapDone = true
    // MainEntry、awake、精灵 Main 首段和 OnLoaded 已处理

runFrameScripts 派发 eventStart
    StartDispatched = true
    // 全局用户 OnStart 已建立快照并完成派发阶段
```

这里的“已派发”不表示所有 `OnStart` 协程已经完整结束。`BatchWaitFirstSlice` 只保证每个处理器至少执行到第一次 Yield/Wait 或直接返回。

## 16. 最终压缩调用图

```text
Godot MainLoop
    |
    +-- start
    |     -> Spx::on_start
    |     -> SpxEngine::on_awake
    |     -> C++ Manager awake/start
    |     -> C++ 回调 Go
    |     -> gdengine.onEngineStart
    |     -> Go Manager.OnStart
    |     -> internal/engine.onStart
    |     -> Game.OnEngineStart
    |     -> T0 加载项目并登记 Bootstrap
    |     -> T1 执行 Bootstrap
    |     -> BootstrapDone
    |     -> 后续帧派发用户 OnStart
    |
    `-- update(delta)
          -> Spx::on_update
          -> SpxEngine::on_update
          -> C++ Manager.on_update
          -> C++ 回调 Go
          -> gdengine.onEngineUpdate
          -> Go Manager.OnUpdate
          -> Go 代理 Sprite.OnUpdate
          -> internal/engine.onUpdate
               -> pending 输入/触发事件切换为 ready
               -> Game.OnEngineBeforeUpdate
               -> 推进逻辑时间
               -> Game.OnEngineUpdate
                    -> 条件/输入事件登记
                    -> OnStart 或 AtFrame 登记
                    -> 第一轮 Go -> Godot 精灵同步
                    -> Godot 物理位置回读
               -> gco.Update
                    -> 恢复等待任务
                    -> 运行 runnable 脚本片段
                    -> 等待脚本 Yield/阻塞/结束
               -> Game.OnEngineRender
                    -> 第二轮视觉同步
                    -> 物理触发事件登记
               -> FlushCaptures
               -> Game.OnEngineFrameEnd
          -> updateTimers/updateTweens
          -> C++ PenMgr.flush_all
          -> 返回 Godot 主循环
```

