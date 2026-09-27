# SPX 引擎启动与 Update 可折叠调用树

本文将完整流程收敛为三棵可折叠调用树：

1. 引擎绑定、启动与 Bootstrap；
2. Godot 每帧 Update；
3. 输入、物理触发等异步事件旁路。

点击每个节点标题即可展开或折叠。节点内部按“源码位置、执行内容、注意事项、子调用”组织。

## 层次图例

| 标记 | 所在层 | 主要职责 |
| --- | --- | --- |
| `[Godot]` | Godot 主循环 | 提供启动、逻辑帧、物理帧和销毁时机 |
| `[C++]` | `godot_modules/spx` | 管理 Godot 节点、资源、物理、渲染和 C++ Manager |
| `[Go代理]` | `internal/gdengine` | 统一 Native/Web 回调，维护 Go 侧 Godot 代理 |
| `[Go核心]` | `internal/engine` | 管理游戏绑定、帧阶段、时间、输入缓存和协程入口 |
| `[Go游戏]` | 根包 `spx` | 管理 `Game`、`SpriteImpl`、事件、Bootstrap 和数据同步 |
| `[协程]` | `internal/coroutine` | 串行运行脚本片段，处理 Wait、Yield、Join 和主线程任务 |

---

## 树一：引擎绑定、启动与 Bootstrap

<details open>
<summary><strong>ROOT：一局游戏从 Go 入口启动到 <code>BootstrapDone</code></strong></summary>

<details>
<summary><code>[Go核心] internal/engine.Main(game, owner, initialize)</code> — 建立一局游戏与 Godot 后端的运行会话</summary>

- 位置：`internal/engine/engine.go:102`
- 执行线程：游戏入口所在 goroutine。
- 执行内容：绑定当前 Game、安装 Manager 包装和回调表、连接 Native/Web 后端。
- 注意：此时主要完成“绑定”，还没有加载具体项目资源。

<details>
<summary><code>bindGameAtPhase(game, owner, gameStarting)</code> — 注册当前活动游戏</summary>

- 将本局保存为 `activeGame`。
- 初始阶段为 `gameStarting`，防止两个 Game 同时占用全局运行时。
- 建立 `gameBinding`，其中保存游戏回调、owner、阶段和后端会话。

</details>

<details>
<summary><code>initialize()</code> — 执行项目生成代码提供的初始化入口</summary>

- 初始化项目运行时对象及相关类型信息。
- 该函数由调用 `internal/engine.Main()` 的上层传入。

</details>

<details>
<summary><code>enginewrap.Init(WaitMainThread)</code> — 初始化 Go Manager 包装层</summary>

- 将底层 Manager 调用统一包装到 `enginewrap`。
- Native 环境中，涉及 Godot 对象和场景树的调用可以通过 `WaitMainThread` 转交主线程。
- 这里不会创建 C++ Manager；C++ Manager 由 `SpxEngine` 创建。

</details>

<details>
<summary><code>CoreCallbackInfo{...}</code> — 构造 Godot 回调到 Go 核心层的函数表</summary>

```text
OnEngineStart     -> internal/engine.onStart
OnEngineUpdate    -> internal/engine.onUpdate
OnEngineDestroy   -> internal/engine.onDestroy
OnEngineDestroyed -> internal/engine.onDestroyed
OnEngineReset     -> internal/engine.onReset
OnEnginePause     -> internal/engine.onPause
OnMousePressed    -> internal/engine.onMousePressed
OnMouseReleased   -> internal/engine.onMouseReleased
OnKeyPressed      -> internal/engine.onKeyPressed
OnKeyReleased     -> internal/engine.onKeyReleased
```

- 此处只是建立函数表，不会立即调用这些函数。

</details>

<details>
<summary><code>[Go代理] gde.PrepareLink(callbacks)</code> — 安装 FFI、公共回调和 Go Manager 代理</summary>

- 位置：`internal/gdengine/linker.go:101`
- 结果：返回本次绑定的 `LinkSession`，并保存为 `gameBinding.link`。

<details>
<summary><code>facade.LinkFFI()</code> — 选择并连接 Native/Web/pure 后端</summary>

- Native：解析 GDExtension 导出的 C 函数地址。
- Web：解析 JS/WASM 桥接函数。
- pure：使用空实现或测试实现。
- 返回的 `interpreter` 仅表示 Web 解释器模式，不表示调用失败。

</details>

<details>
<summary><code>coreCallbacks = callbacks</code> — 保存 Go 核心层回调</summary>

- `internal/gdengine` 后续收到平台事件时，通过该回调表继续进入 `internal/engine`。

</details>

<details>
<summary><code>facade.RegisterCallbacks(bindCallbacks())</code> — 向平台后端注册统一回调表</summary>

- `bindCallbacks()` 包含引擎、精灵、UI、输入、碰撞和触发器回调。
- Native 最终保存 Go 导出函数指针；Web 最终保存 JS/WASM 分发入口。

</details>

<details>
<summary><code>engineimpl.CreateMgrs()</code> — 创建 Go 侧 Godot Manager 代理</summary>

```text
AudioMgr、CameraMgr、DebugMgr、ExtMgr、InputMgr、NavigationMgr、PenMgr、
PhysicsMgr、PlatformMgr、ResMgr、SceneMgr、SpriteMgr、TilemapMgr、UiMgr 等
```

- 这些对象是 Go 代理，不是 C++ `SpxXXXMgr` 本体。

</details>

<details>
<summary><code>engineimpl.BindMgr(mgrs)</code> — 将代理写入全局 Manager 接口变量</summary>

- 业务和引擎代码随后通过 `engine.Managers()` 或 `pkg/spx/pkg/engine` 接口调用 Manager。
- 平台差异由绑定的具体代理实现和 FFI 层吸收。

</details>

</details>

<details>
<summary><code>binding.link.Run(finishStart)</code> — 启动平台会话</summary>

- Native/pure 通常完成状态确认后返回。
- Web 会话可能持续阻塞到 `Unlink()`。
- Web 解释器模式没有真实 C++ `OnEngineStart`，会在这里主动补发同名事件。

</details>

</details>

<details>
<summary><code>[C++] SpxEngine::register_callbacks()</code> — 创建 C++ SpxEngine 单例及全部 Manager</summary>

- 位置：`godot_modules/spx/spx_engine.cpp:84`
- 直接来源：Native 全局回调注册或 Web 模块初始化。
- 执行内容：创建唯一 `SpxEngine`、初始化 C++ Manager、保存 Go/JS 回调表。

<details>
<summary><code>SpxEngine::_initialize_managers()</code> — 按依赖顺序创建 C++ Manager</summary>

```text
SpxInputMgr
SpxAudioMgr
SpxPhysicsMgr
SpxSpriteMgr
SpxUiMgr
SpxSceneMgr
SpxCameraMgr
SpxPlatformMgr
SpxResMgr
SpxDebugMgr
SpxNavigationMgr
SpxPenMgr
SpxTilemapMgr
SpxTilemapparserMgr
```

- 位置：`godot_modules/spx/spx_engine.cpp:506`
- `SpxEngine.managers` 拥有这些对象；快捷成员指针只是非拥有引用。
- 销毁时按创建顺序的逆序释放。

</details>

</details>

<details open>
<summary><code>[Godot] MainLoopPhaseCallbackBus -> _spx_main_loop_start()</code> — Godot SceneTree 启动 SPX</summary>

- 位置：`godot_modules/spx/spx.cpp:65`
- 执行线程：Godot 主线程。

<details>
<summary><code>[C++] Spx::on_start(MainLoop*)</code> — 创建 SPX 场景树锚点</summary>

- 位置：`godot_modules/spx/spx.cpp:162`
- 校验 `MainLoop` 是 `SceneTree`，并取得根 `Window`。

<details>
<summary><code>memnew(SpxEngineNode)</code> + <code>root->add_child()</code> — 挂载 SPX 根节点</summary>

- `SpxEngineNode` 是大部分 SPX 节点的场景树锚点。
- 加入树后由 `SceneTree` 管理节点生命周期。
- `SpxEngine::set_root_node()` 保存 `SceneTree`、根节点和延迟回调代理。

</details>

<details open>
<summary><code>SpxEngine::on_awake()</code> — 启动 C++ Manager 后回调 Go</summary>

- 位置：`godot_modules/spx/spx_engine.cpp:160`
- 顺序不能颠倒：Manager 必须先准备好，Go 项目加载才能调用它们。

<details>
<summary><code>_notify_managers(SpxManager::on_awake)</code> — 建立 Manager 的 Godot 节点和资源</summary>

- 例如创建输入代理、精灵根节点、UI `CanvasLayer`、画笔画布、相机或调试根节点。

</details>

<details>
<summary><code>_notify_managers(SpxManager::on_start)</code> — 完成 C++ Manager 启动生命周期</summary>

- 与用户脚本 `OnStart` 无关。
- `SpxSpriteMgr::on_start()` 也会启动已存在的 C++ 精灵包装对象。

</details>

<details open>
<summary><code>callbacks.func_on_engine_start()</code> — 跨 C++/Go 边界发出启动回调</summary>

<details>
<summary><code>[Native] func_on_engine_start()</code> — C ABI 入口</summary>

- 位置：`internal/gdengine/binding/native/gdextension_interface.go:649`
- 将 C++ 回调转发给已注册的 `callbacks.OnEngineStart`。

</details>

<details open>
<summary><code>[Go代理] gdengine.onEngineStart()</code> — 先启动 Go Manager 代理，再进入 Go 核心层</summary>

- 位置：`internal/gdengine/callbacks.go:91`

<details>
<summary><code>for _, mgr := range mgrs { mgr.OnStart() }</code> — Go Manager 代理启动</summary>

- 调用的是 Go 侧 Manager 代理生命周期。
- 它既不是 C++ `SpxManager::on_start()`，也不是用户脚本 `OnStart`。

</details>

<details open>
<summary><code>coreCallbacks.OnEngineStart()</code> -> <code>[Go核心] internal/engine.onStart()</code></summary>

- 位置：`internal/engine/engine.go:145`

<details>
<summary><code>ResetInputState()</code> — 清空键盘和鼠标全局状态</summary>

- 防止上一局的按住状态和 pending 边沿进入新一局。

</details>

<details>
<summary><code>resetTriggerEvents()</code> — 清空触发器 pending/ready 队列</summary>

- 防止上一局的物理触发结果被新一局消费。

</details>

<details>
<summary><code>itime.Start(PlatformMgr.SetTimeScale)</code> — 初始化 SPX 逻辑时间</summary>

- 建立帧号、逻辑时间和时间缩放基础状态。

</details>

<details open>
<summary><code>[Go游戏] Game.OnEngineStart()</code> — 创建异步项目启动协程 T0</summary>

- 位置：`runtime_engine.go:36`
- `RunOnce` 保证同一局只进入一次。
- 捕获 `bootstrapGeneration`，reset 后旧任务会因代际不匹配失效。

<details open>
<summary><code>engine.Go(Game, fn)</code> — 通过 <code>gco.Create()</code> 创建启动协程 T0</summary>

- `Create()` 先把 Thread 登记为 `runnable`，再启动底层 Go goroutine。
- T0 的实际开始时间取决于 Go 调度和 `runMu` 竞争，不要求等到 `gco.Update()` 才启动。
- `gco.Update()` 若先看到 T0 为 runnable，会等待 T0 Yield、阻塞或结束。

<details>
<summary><code>isCurrentBootstrap(generation)</code> — 校验 T0 仍属于当前一局</summary>

- reset/reload 会增加代际编号。
- 旧代际协程不能继续修改新 Game。

</details>

<details>
<summary><code>queueBootstrap(MainEntry)</code> — 将项目级 Main 登记到启动函数队列</summary>

- `pendingBootstrap` 保存的是 `[]func()`，不是 Thread 或 WaitJob 队列。
- 真正执行者是后面的 Bootstrap 协程 T1。

</details>

<details open>
<summary><code>loadGame("assets", generation)</code> — 加载项目并建立初始 Go/Godot 对象</summary>

- 位置：`game_build.go:38`
- 该节点将原文中分散的 `loadGame` 调用全部并入启动树。

<details>
<summary><code>coreproject.OpenBuilderResources()</code> — 打开项目配置、字体和 assets 文件系统</summary>

- 返回项目配置、构建配置、字体列表和项目 FS。
- 失败会结束当前启动协程并进入引擎错误处理。

</details>

<details>
<summary><code>engine.SetAssetDir()</code> — 设置资源路径基准</summary>

- 后续服装、声音、字体等资源通过此基准解析。

</details>

<details>
<summary><code>applyRuntimeFontPlan()</code> — 通过 ResMgr 应用项目字体</summary>

- 解析默认字体和字体缓存计划。
- 涉及 Godot Resource 的操作必须满足主线程约束。

</details>

<details>
<summary><code>parseCommandLineFlags()</code> + <code>applyRuntimeConfig()</code> — 应用启动参数和项目配置</summary>

- 配置窗口、运行参数、调试模式及项目级设置。

</details>

<details>
<summary><code>setupGameSystems()</code> — 配置物理、音频、寻路和渲染层排序</summary>

- 调用 `applyPathFinderSettings()`、`applyAudioSettings()`、`applyPhysicsSettings()`。
- 这里只配置系统，不执行精灵生命周期脚本。

</details>

<details open>
<summary><code>loadGameSprites()</code> — 反射遍历 Gamer 字段并创建 Go 精灵对象</summary>

- 位置：`game_build.go:108`

<details>
<summary><code>startLoad(fs)</code> — 初始化项目级 Go 子系统</summary>

- 初始化 `soundMgr`、声音表、`inputMgr`、`Game.events` 和项目文件系统引用。
- `Game.events` 是部分高层事件使用的通道，不是所有事件唯一入口。

</details>

<details>
<summary><code>coreproject.WalkFields()</code> — 查找生成 Gamer 中的精灵字段</summary>

- 根据字段和类型表定位项目声明的具体精灵对象。

</details>

<details>
<summary><code>loadSprite()</code> -> <code>loadSpriteConfig()</code> — 初始化每个 SpriteImpl</summary>

- 加载精灵配置。
- `SpriteImpl.init()` 初始化组件和运行状态。
- 通过 `SpriteMgr` 创建 `internal/engine.Sprite` 代理及 Godot `SpxSprite` 节点。
- 将业务精灵记录到 `Game.sprs`，随后加入 `shapeManager`。

</details>

<details>
<summary><code>tilemapMgr.init()</code> — 初始化 Go TileMap 数据</summary>

- 保存地图 FS 和配置，实际应用在舞台设置阶段进行。

</details>

</details>

<details open>
<summary><code>loadStage()</code> — 建立舞台、相机、TileMap、Z 序并登记生命周期任务</summary>

- 位置：`game_load.go:47`

<details>
<summary><code>setupDisplayConfig()</code> — 解析窗口缩放、拉伸和调试配置</summary>

- 更新 `displayState` 和日志级别。

</details>

<details>
<summary><code>setupWorldAndWindow()</code> — 初始化世界大小、背景和窗口尺寸</summary>

- 有 TileMap 时可能不用普通 backdrop。
- 计算 SPX 世界坐标与 Godot 窗口尺寸之间的映射。

</details>

<details>
<summary><code>setupPlatformAndCamera()</code> — 配置窗口、相机和舞台背景代理</summary>

- 设置全屏、窗口大小、最大 FPS 和拉伸模式。
- 创建 `cameraImpl`。
- 创建舞台背景的 `engine.Sprite` 代理。

</details>

<details>
<summary><code>setupAudioAndTilemap()</code> — 应用 TileMap 并分配音频对象</summary>

- `applyTilemap()` 解析/创建地图层并更新场景几何。
- 初始化声音对象，必要时播放 BGM。

</details>

<details>
<summary><code>loadAndInitSprites()</code> — 按项目 Z 序加入 Shape</summary>

- 加载普通精灵和特殊 Shape。
- 设置精灵层级。
- 加入 `shapeManager.items`。
- 重建连续渲染层。

</details>

<details open>
<summary><code>runSpriteCallbacks()</code> — 将碰撞、Awake、Main、OnLoaded 写入 pendingBootstrap</summary>

```text
queueBootstrap(setupCollisionData)
queueBootstrap(sprite.awake)       // 每个精灵一个任务
queueBootstrap(runSpriteMainsUntilYield)
queueBootstrap(Game.OnLoaded)
```

- 位置：`game_load.go:225`
- 加上此前登记的 `MainEntry`，构成完整 Bootstrap 顺序。
- 此处只登记，不执行这些函数。

</details>

</details>

<details>
<summary><code>initEventLoop()</code> — 创建输入事件循环协程</summary>

- 位置：`runtime_loops.go:27`
- 输入循环等待键盘/鼠标状态和事件，再进入脚本事件系统。
- 它是长期协程，会在无事件时挂起，不会持续阻止 `gco.Update()` 返回。

</details>

<details>
<summary><code>PlatformMgr.SetWindowTitle()</code> — 设置 Godot 窗口标题</summary>

- 底层 Godot API 需要遵守主线程调用约束。

</details>

</details>

<details>
<summary><code>markGameStarted(generation)</code> — 设置 <code>IsRunned = true</code></summary>

- 表示资源、舞台和初始代理已经建立，`Game.OnEngineUpdate()` 可以进入。
- 不表示 Bootstrap 已完成，也不表示用户 `OnStart` 已派发。

</details>

<details open>
<summary><code>startBootstrap(generation)</code> — 创建 Bootstrap 协程 T1</summary>

- 位置：`runtime_engine.go:353`
- 通过第二次 `engine.Go(Game, fn)` 创建受管 Thread。

<details>
<summary><code>claimBootstrap(generation)</code> — 抢占本代际唯一执行权</summary>

- 防止重复路径同时执行 Bootstrap 队列。

</details>

<details open>
<summary><code>runBootstrapTasks(generation)</code> — 按顺序执行所有 pendingBootstrap 函数</summary>

- 位置：`runtime_engine.go:250`
- T1 是这些普通 `func()` 的直接执行者。
- 每轮先取出当前批次并清空队列；任务中新登记的 Bootstrap 函数会在下一轮取出。

<details>
<summary><code>MainEntry</code> — 执行项目级 Main 的第一个脚本片段</summary>

<details>
<summary><code>runMainUntilYield(Game, MainEntry)</code> — 创建 Main 子协程并等待首段</summary>

- `gco.Create()` 创建子协程 T2。
- `gco.JoinYieldedOrDone(T2)` 将 T1 标为 blocked，并登记到 T2 的 `yieldWaiters`。
- T2 首次 `Yield/Wait/WaitNextFrame` 或直接结束时唤醒 T1。
- 不等待 T2 的完整生命周期。

</details>

</details>

<details>
<summary><code>setupCollisionData(inits)</code> — 初始化所有初始精灵的碰撞配置</summary>

- 准备碰撞/感应形状、Layer、Mask 等数据。

</details>

<details>
<summary><code>sprite.awake</code> — 逐个执行精灵 Awake 生命周期</summary>

- 用于精灵自身的早期初始化和相关事件处理。
- 不等于全局用户 `OnStart`。

</details>

<details>
<summary><code>runSpriteMainsUntilYield(inits)</code> — 按加载/Z 序运行每个精灵 Main 首段</summary>

- 每个精灵再次通过 `runMainUntilYield()` 创建独立 Main 子协程。
- 当前精灵 Main 首次让出或结束后，才处理下一个精灵。
- Main 中注册的 `OnStart`、`OnClick`、`OnMsg` 等进入事件注册表。

</details>

<details>
<summary><code>Game.OnLoaded</code> — 执行项目加载完成回调</summary>

- 这是 Bootstrap 队列的最后一个初始任务。
- 它直接在 T1 当前执行片段中调用；若其内部使用脚本等待 API，需要有受管协程上下文。

</details>

</details>

<details>
<summary><code>completeBootstrap(generation)</code> — 设置 <code>BootstrapDone = true</code></summary>

- 只有 `runBootstrapTasks()` 返回后才执行。
- 用户 `OnStart` 不在此处同步调用，而是在后续 `Game.OnEngineUpdate()` 的 `runFrameScripts()` 中派发。

</details>

</details>

</details>

</details>

</details>

</details>

</details>

</details>

</details>

</details>

</details>

### 树一的关键时序结论

```text
IsRunned = true
    表示 loadGame 已完成，可以进入 Game.OnEngineUpdate

BootstrapDone = true
    表示 MainEntry、碰撞初始化、Awake、精灵 Main 首段和 OnLoaded 已处理

StartDispatched = true
    表示后续帧已经取得 OnStart 快照并完成启动事件派发阶段
```

---

## 树二：Godot 每帧 Update

<details open>
<summary><strong>ROOT：Godot 一次普通逻辑帧进入 Go、运行脚本并返回</strong></summary>

<details open>
<summary><code>[Godot] MainLoopPhaseCallbackBus -> _spx_main_loop_update(delta)</code></summary>

- 位置：`godot_modules/spx/spx.cpp:76`
- 执行线程：Godot 主线程。

<details open>
<summary><code>[C++] Spx::on_update(delta)</code> — 先消费跨线程控制命令</summary>

- 位置：`godot_modules/spx/spx.cpp:197`

<details>
<summary><code>pending_controls.take(RESTART/RESET)</code> — 高优先级生命周期控制</summary>

- 命中 restart/reset 后立即执行并返回，本帧不再进入普通 Update。
- 其他线程只能提交控制命令，真正的 SceneTree 操作仍由 Godot 主线程完成。

</details>

<details>
<summary><code>pending_controls.take(PAUSE/RESUME/NEXT_FRAME)</code> — 更新暂停和单帧状态</summary>

- Pause 状态可能阻止后面的 `SpxEngine::on_update()`。

</details>

<details open>
<summary><code>SpxEngine::on_update(delta)</code> — C++ Manager 更新并回调 Go</summary>

- 位置：`godot_modules/spx/spx_engine.cpp:194`

<details>
<summary><code>has_exit / is_spx_paused</code> 检查 — 决定是否跳过本帧</summary>

- 已永久退出时不再发出常规回调。
- 暂停且没有 `should_execute_single_frame` 时直接返回。

</details>

<details>
<summary><code>_notify_managers(SpxManager::on_update, delta)</code> — 更新所有 C++ Manager</summary>

- 发生在 Go 回调之前。
- 典型工作包括 C++ 音频、场景、精灵、画笔和调试对象的逐帧维护。

</details>

<details open>
<summary><code>callbacks.func_on_engine_update(delta)</code> — 跨 C++/Go 边界</summary>

<details>
<summary><code>[Native] func_on_engine_update(delta)</code> — C ABI 转发入口</summary>

- 位置：`internal/gdengine/binding/native/gdextension_interface.go:658`
- Web 使用 JS/WASM 的 `gdspxDispatch` 完成等价转发。

</details>

<details open>
<summary><code>[Go代理] gdengine.onEngineUpdate(delta)</code> — 更新 Go 代理层</summary>

- 位置：`internal/gdengine/callbacks.go:101`

<details>
<summary><code>itime.EffectiveLogicalDeltaTime(delta)</code> — 确定本帧逻辑步长</summary>

- 普通模式使用 Godot delta。
- 输入回放期间可替换为固定逻辑步长。

</details>

<details>
<summary><code>mgr.OnUpdate(delta)</code> — 遍历 Go Manager 代理</summary>

- Go 代理层生命周期钩子，默认实现可能为空。
- 与此前已经执行的 C++ `SpxManager::on_update()` 不是同一个调用。

</details>

<details>
<summary><code>AdvanceTimeSinceGameStart(delta)</code> — 推进代理层运行时间</summary>

- 用于 Go Godot 代理对象的时间统计。

</details>

<details>
<summary><code>Sprites()</code> -> <code>sprite.OnUpdate(delta)</code> — 更新 Go 侧 Godot 精灵代理</summary>

- `Sprites()` 来源是 `internal/engine.state.sprites`。
- 先复制稳定快照，避免更新中创建/删除精灵改变本轮遍历。
- 这里的对象是实现 `gdx.ISpriter` 的 Go 代理，不是业务层 `SpriteImpl`。

</details>

<details open>
<summary><code>coreCallbacks.OnEngineUpdate(delta)</code> -> <code>[Go核心] internal/engine.onUpdate(delta)</code></summary>

- 位置：`internal/engine/engine.go:167`
- 这是完整 Go 游戏帧的总调度入口。

<details>
<summary><code>CheckPanic + updateMu + updateBusy</code> — 建立帧执行边界</summary>

- `CheckPanic()` 将 panic 转交引擎错误处理。
- `updateMu` 防止同一局重入执行多个 Update。
- `updateBusy` 让 reset/destroy 知道当前帧尚未退出。

</details>

<details>
<summary><code>runningBinding()</code> — 校验当前 Game 仍处于 <code>gameRunning</code></summary>

- 每个主要阶段后都会重新校验，因为脚本可能触发 reset、reload 或 destroy。

</details>

<details>
<summary><code>cacheTriggerEvents/cacheKeyEvents/cacheMouseEvents</code> — 建立本帧 ready 快照</summary>

- 将回调阶段写入的 `pending` 切换为本帧可消费的 `ready`。
- 此处不运行用户事件处理器。
- 该帧边界保证消费过程中不会混入稍后到达的新事件。

</details>

<details open>
<summary><code>[Go游戏] Game.OnEngineBeforeUpdate(delta)</code> — 时间推进前采样输入和条件</summary>

- 位置：`runtime_engine.go:102`

<details>
<summary><code>pendingConditions = nil</code> — 清除旧条件快照</summary>

- 防止输入会话未开启本帧时重复使用上一帧条件结果。

</details>

<details>
<summary><code>inputMgr.prepareInputSessionTick()</code> — 准备录制/回放输入帧</summary>

- 录制模式读取底层 ready 输入。
- 回放模式解析记录中的确定性输入状态。
- 只准备本帧输入，不立即执行用户处理器。

</details>

<details>
<summary><code>scriptEvents.sampleConditions()</code> — 评估 OnCond 上升沿</summary>

- 只有 `StartDispatched` 后才采样，确保 OnStart 已完成初始化和条件注册。
- 保存命中的 `eventSink`，处理器稍后在 `OnEngineUpdate` 中创建。

</details>

</details>

<details>
<summary><code>itime.Update(delta, fps)</code> — 推进 SPX 逻辑帧和逻辑时间</summary>

- `OnEngineBeforeUpdate()` 看见的是时间推进前状态。
- 后面的事件派发和脚本执行看见的是推进后的本帧时间。

</details>

<details open>
<summary><code>[Go游戏] Game.OnEngineUpdate(delta)</code> — 登记本帧脚本并进行第一轮双向同步</summary>

- 位置：`runtime_engine.go:125`
- 注意：该函数在 `gco.Update()` 之前执行；通常只创建/登记事件协程，不保证用户函数已经执行。

<details>
<summary><code>IsRunned/input session</code> 检查 — 决定是否进入本帧逻辑</summary>

- `IsRunned == false` 表示项目尚未完成资源加载。
- 输入会话没有准备好当前 tick 时跳过，避免重复使用旧输入。

</details>

<details>
<summary><code>scriptEvents.dispatchConditions()</code> — 为命中的条件事件创建协程</summary>

- 使用 `OnEngineBeforeUpdate()` 已保存的快照，不重新求值。
- 新 Thread 被登记为 runnable，实际运行服从 `runMu` 和 `gco.Update()`。

</details>

<details>
<summary><code>inputMgr.dispatchInputSessionTick()</code> — 派发录制/回放输入事件</summary>

- 将本 tick 的有效输入转换为高层事件。
- 普通实时输入主要由长期 `inputEventLoop` 消费。

</details>

<details>
<summary><code>soundMgr.Update()</code> — 更新 Go 游戏层声音状态</summary>

- 与 C++ `SpxAudioMgr::on_update()` 属于不同层次。

</details>

<details open>
<summary><code>runFrameScripts()</code> — 派发首次 OnStart 或到期 AtFrame 回调</summary>

- 位置：`runtime_engine.go:182`
- 更准确的职责是“推进本帧脚本入口阶段”。

<details open>
<summary><code>BootstrapDone && !StartDispatched</code> — 本帧只派发 OnStart</summary>

<details>
<summary><code>dispatchStartEventIfNeeded()</code> — 生成并处理一次性 eventStart</summary>

<details>
<summary><code>scheduleStartEvent()</code> — 原子检查启动派发状态</summary>

- 同时检查 `BootstrapDone`、`StartDispatched` 和 `startScheduled`。
- `startScheduled` 防止派发器尚未运行时重复创建第二个 `eventStart`。

</details>

<details open>
<summary><code>handleEvent(eventStart)</code> — 创建 OnStart 派发器协程</summary>

<details>
<summary><code>gco.Create(startEventDispatcher, runStartPhase)</code> — 登记派发器 Thread</summary>

- 当前调用来自 Godot 主线程，不同步 `Join`，避免派发器中的主线程调用形成死锁。
- 后面的 `gco.Update()` 会观察该 runnable Thread。

</details>

<details>
<summary><code>takeStartSinks(generation)</code> — 一次性冻结全局 OnStart 注册</summary>

- `SnapshotStartOnce()` 取得稳定快照并关闭后续全局启动注册。
- 运行期克隆精灵的迟到 `OnStart` 不会重放，应使用 `OnCloned`。

</details>

<details>
<summary><code>doWhenStart()</code> -> <code>StartBatch(BatchWaitFirstSlice)</code></summary>

- 按当前 Scratch 目标顺序整理处理器。
- 为每个处理器创建独立协程。
- `BatchWaitFirstSlice` 等待每个 OnStart 至少执行到首次 Yield/Wait 或结束。
- 不等待所有 OnStart 的完整生命周期。

</details>

<details>
<summary><code>markStartDispatched()</code> — 设置 <code>StartDispatched = true</code></summary>

- 表示启动事件派发阶段完成，不表示所有 OnStart 永久结束。

</details>

</details>

</details>

- `runFrameScripts()` 随即 `return`，首次 OnStart 派发帧不同时取出 AtFrame 回调。

</details>

<details>
<summary><code>其他状态 -> engine.RunFrameCallbacks()</code> — 取出到期 AtFrame 回调</summary>

<details>
<summary><code>frameCallbackQueue.takeDue(CurrentFrame)</code> — 分离到期和未来回调</summary>

- `targetFrame <= CurrentFrame` 的回调被取出。
- 未来帧回调继续留在队列。
- 到期回调按目标帧稳定排序，同帧保持注册顺序。

</details>

<details>
<summary><code>executeFrameCallbacks()</code> — 为回调建立受管协程上下文</summary>

- 从引擎线程调用时先创建一个分发协程。
- 每个回调随后通过 `gco.Create(callback.owner, callback.fn)` 创建独立 Thread。
- 来源 Thread 已停止时取消对应回调。

</details>

</details>

<blockquote>
细节：当 <code>BootstrapDone == false</code> 时，OnStart 条件不成立，代码仍会执行
<code>RunFrameCallbacks()</code>。因此绝对帧号已经到期的 AtFrame 回调可能在 Bootstrap
完成前被取出。只有首次派发 OnStart 的那一帧会暂停普通 AtFrame 回调一次。
</blockquote>

</details>

<details open>
<summary><code>updateSpriteProxies()</code> — gco.Update 前的第一轮 Go -> Godot 同步</summary>

- 位置：`runtime_sync.go:105`

<details>
<summary><code>camera.onUpdate()</code> — 更新相机代理状态</summary>

- 收集相机目标、位置和限制变化。

</details>

<details>
<summary><code>shapeMgr.getTempShapes()</code> — 获取稳定 Shape 快照</summary>

- 避免遍历时创建、销毁或激活 Shape 改变当前集合。

</details>

<details>
<summary><code>shapeMgr.flushActivate()</code> — 推进 Shape 激活状态</summary>

- 处理需要公开、隐藏或激活的运行对象。

</details>

<details open>
<summary><code>flushSpriteProxyChanges(activeShapes)</code> — 收集并批量提交精灵变化</summary>

<details>
<summary><code>syncBuffer.Clear()</code> — 清空可复用缓冲区</summary>

- `SpriteSyncBuffer` 不用于长期保存状态，而是本轮同步的暂存/序列化缓冲区。

</details>

<details>
<summary><code>shapeMgr.collectProxyUpdates()</code> — 收集精灵脏数据</summary>

- 包括 ID、位置、旋转、缩放、渲染偏移和可见性等。

</details>

<details>
<summary><code>shapeMgr.flushDestroy()</code> — 追加待删除对象 ID</summary>

- 来源是 `shapeManager.destroyItems`。
- 删除 ID 与更新项一起进入同一批次。

</details>

<details>
<summary><code>flushSyncBuffer()</code> -> <code>engine.SyncBatchUpdateSprites()</code></summary>

- `Serialize()` 编码 `[updateCount, deleteCount, updates..., deleteIDs...]`。
- 通过 Go Manager/FFI 到达 Godot `SpxSpriteMgr`。
- Godot 侧先校验完整数据包，再执行删除和属性更新。

</details>

</details>

</details>

<details open>
<summary><code>pullPhysicsPositions()</code> — Godot 物理位置回读到 Go</summary>

- 位置：`runtime_sync.go:186`
- 与前面的 Go -> Godot 同步方向相反。

<details>
<summary><code>coreruntime.SyncBatchPositions()</code> — 筛选、查询并应用位置</summary>

<details>
<summary><code>shouldPullPhysicsPosition()</code> — 选择启用 Godot 物理驱动的 SpriteImpl</summary>

- 普通纯渲染精灵不需要回读位置。

</details>

<details>
<summary><code>syncBuffer.GetPositions(ids)</code> — 调用 Godot BatchRetrievePositions</summary>

- 使用精灵代理 ID 查询 Godot 侧物理节点位置。
- 返回数组为 `[x0, y0, x1, y1, ...]`。

</details>

<details>
<summary><code>SpriteImpl.applyPhysicsPosition(x, y)</code> — 回写业务状态</summary>

- 将 Godot 坐标转换后的物理结果写回 Go 精灵。
- 无效/NaN 项会被跳过。

</details>

</details>

</details>

</details>

<details open>
<summary><code>[协程] gco.Update()</code> — 处理到期 WaitJob 并等待 runnable 脚本让出</summary>

- 位置：`internal/coroutine/update.go:52`
- 它不是单纯的“启动协程”接口。
- 返回条件：没有仍需等待的 runnable Thread，且没有本轮可处理任务。

<details>
<summary><code>beginUpdate()</code> — 固定本轮调度快照和预算</summary>

- 保存当前逻辑帧、关卡时间、同帧工作预算和一秒 watchdog。
- 本轮循环中不会重复读取逻辑时间。

</details>

<details open>
<summary><code>runUpdateLoop()</code> — 重复选择任务、等待状态变化或结束本轮</summary>

<details>
<summary><code>nextUpdateAction()</code> — 在 schedulerMu 下观察队首和 runnableThreads</summary>

```text
队首是 waitTypeMainThread
    -> 即使存在 runnable，也优先执行主线程任务
    -> 防止脚本持有 runMu 等待主线程而双方死锁

存在 runnable Thread
    -> schedulerCond.Wait()
    -> 等待它 Yield、进入 blocked 或 finish

没有 runnable 且没有队首任务
    -> 当前 Update 可完成，或尝试同帧下一脚本轮次
```

</details>

<details>
<summary><code>processWaitJob()</code> — 按等待类型决定恢复时机</summary>

| 类型 | 行为 |
| --- | --- |
| `waitTypeFrame` | 至少跨过一个逻辑帧后恢复 |
| `waitTypeTime` | 时间到且至少跨帧后恢复 |
| `waitTypeYield` | 已获准在当前 Update 恢复 |
| `waitTypeLoop` | 可以驱动同帧额外脚本轮次 |
| `waitTypeNextRound` | 可跟随额外轮次，但不主动驱动 |
| `waitTypeMainThread` | 在引擎主线程执行任务，不走普通 `Resume` |

</details>

<details>
<summary><code>markRunnableAndResume()</code> — 先发布 runnable，再唤醒底层 goroutine</summary>

- 顺序必须是“状态先可见，Resume 后执行”。
- 防止 `gco.Update()` 在 goroutine 真正恢复前误判没有工作而提前返回。

</details>

<details>
<summary><code>watchdog</code> — 单次 Update 最多等待约一秒</summary>

- 超时会将剩余工作延后，不会直接取消所有脚本。
- 无 Yield 的 CPU 死循环仍可能占住 `runMu`，协作式调度无法强制抢占业务代码。

</details>

</details>

<details>
<summary><code>promoteDeferredJobs()</code> — 保存未来帧任务</summary>

- 合并同帧轮次任务和未来任务。
- 按 Thread 顺序稳定排序，留给下一次 `gco.Update()`。

</details>

<blockquote>
<code>Create()</code> 会先登记 runnable，再启动 Go goroutine。因此可能出现 Thread 已是
runnable，但还没获得 CPU 或 <code>runMu</code>，而 <code>gco.Update()</code> 已在等待它。
这不是 Update 主动调用该 Thread，而是 Update 等待 Thread 的执行片段完成交接。
</blockquote>

</details>

<details open>
<summary><code>[Go游戏] Game.OnEngineRender(delta)</code> — 协程运行后的渲染前准备</summary>

- 位置：`runtime_engine.go:155`
- 不是 Godot 真正绘制函数；它负责在 Godot 绘制前提交最终数据。

<details>
<summary><code>defer flushPenCommands()</code> — 提交 Go 侧画笔命令</summary>

- 函数退出时批量发送画笔更新。
- C++ `SpxEngine::on_update()` 返回 Go 后还会执行 `pen->flush_all()`，把命令落实到画布。

</details>

<details>
<summary><code>shapeMgr.takeCloneProxyPublications()</code> — 公开已完成首段初始化的克隆</summary>

- 克隆在 Main/OnCloned 首段初始化完成前可以保持不公开，避免中间状态进入渲染。

</details>

<details open>
<summary><code>syncPostCoroutineVisuals()</code> — 第二轮 Go -> Godot 视觉同步</summary>

- 位置：`runtime_sync.go:121`
- 补交 `gco.Update()` 中用户脚本刚产生的视觉变化。

<details>
<summary><code>camera.onUpdate()</code> + <code>flushSpriteProxyChanges()</code></summary>

- 再次收集位置、服装、可见性等变化。
- 不重复推进逻辑时间和脚本帧。

</details>

<details>
<summary><code>shapeMgr.flushBubbleVisuals()</code> — 使用最终精灵边界布局气泡</summary>

- 放在代理同步之后，确保气泡读取本帧最终位置和尺寸。

</details>

</details>

<details>
<summary><code>processPhysicsTriggers()</code> — 消费 ready 物理触发事件</summary>

- 调用 `engine.GetTriggerEvents()` 取出稳定快照。
- 过滤隐藏、销毁中或无效的精灵。
- `fireTouchStart()` 创建对应用户处理器协程。
- 因为本帧 `gco.Update()` 已经结束，新处理器通常到下一帧才执行。

</details>

</details>

<details>
<summary><code>FlushCaptures()</code> — 提交本帧截图请求</summary>

- 位于第二轮视觉同步之后，截图能观察本帧最终状态。

</details>

<details>
<summary><code>Game.OnEngineFrameEnd()</code> — 完成输入会话帧末处理</summary>

- 当前主要用于录制/回放完成状态转换。

</details>

</details>

<details>
<summary><code>InternalUpdateEngine(delta)</code> — 更新 Go 代理层 Timer 和 Tween</summary>

- 调用位置：`gdengine.onEngineUpdate()` 中，位于 `coreCallbacks.OnEngineUpdate()` 返回之后。
- 内部执行 `updateTimers(delta)` 和 `updateTweens(delta)`。
- 这意味着它发生在完整 `internal/engine.onUpdate()` 之后。

</details>

</details>

</details>

<details>
<summary><code>[C++] pen->flush_all()</code> — 将画笔命令落实到 Godot 渲染目标</summary>

- Go 的整个更新回调返回到 `SpxEngine::on_update()` 后执行。

</details>

<details>
<summary><code>返回 Godot MainLoop</code> — Godot 使用最新节点状态继续渲染</summary>

- 至此 SPX 的本帧逻辑阶段结束。

</details>

</details>

</details>

</details>

</details>

### 树二的压缩视图

```text
Godot MainLoop
`-- Spx::on_update
    `-- SpxEngine::on_update
        +-- C++ Manager.on_update
        +-- C++ -> Go 回调
        |   `-- gdengine.onEngineUpdate
        |       +-- Go Manager.OnUpdate
        |       +-- Go 代理 Sprite.OnUpdate
        |       +-- internal/engine.onUpdate
        |       |   +-- pending -> ready
        |       |   +-- Game.OnEngineBeforeUpdate
        |       |   +-- itime.Update
        |       |   +-- Game.OnEngineUpdate
        |       |   |   +-- 条件/输入事件登记
        |       |   |   +-- OnStart 或 AtFrame 登记
        |       |   |   +-- 第一轮 Go -> Godot 同步
        |       |   |   `-- Godot 物理位置 -> Go
        |       |   +-- gco.Update
        |       |   +-- Game.OnEngineRender
        |       |   |   +-- 第二轮 Go -> Godot 同步
        |       |   |   `-- 物理触发处理器登记
        |       |   +-- FlushCaptures
        |       |   `-- Game.OnEngineFrameEnd
        |       `-- updateTimers/updateTweens
        `-- C++ PenMgr.flush_all
```

---

## 树三：异步事件如何接入 Update

<details open>
<summary><strong>ROOT：Godot 回调先写 pending，Go 帧边界再缓存和派发</strong></summary>

<details>
<summary><code>键盘事件</code> — Godot InputEvent 到用户 OnKey 协程</summary>

```text
Godot InputEvent
`-- C++ SpxInputMgr/SpxInputProxy
    `-- func_on_key_pressed/released
        `-- gdengine.onKeyPressed/released
            `-- internal/engine.onKeyPressed/released
                +-- 更新 pressed 状态
                `-- keyInput.pending 追加有序边沿
                    `-- 下一次 internal/engine.onUpdate
                        `-- cacheKeyEvents: pending -> ready
                            `-- inputEventLoop/输入会话消费
                                `-- scriptEventRegistry 匹配 OnKey
                                    `-- gco.Create 用户处理器 Thread
                                        `-- gco.Update 实际运行
```

- 底层回调不直接执行用户脚本。
- `pending -> ready` 把连续到达的输入固定为稳定帧快照。

</details>

<details>
<summary><code>鼠标事件</code> — 按钮状态、边沿和点击处理</summary>

```text
Godot Mouse InputEvent
`-- C++ 输入代理
    `-- func_on_mouse_pressed/released
        `-- gdengine.onMousePressed/released
            `-- internal/engine.onMousePressed/released
                +-- 立即更新 button pressed 状态
                `-- 捕获开启时追加 mouseInput.pending
                    `-- 下一次 onUpdate: cacheMouseEvents
                        `-- 输入循环/输入会话
                            `-- 命中目标和手势判断
                                `-- OnClick/OnSwipe 等处理器 Thread
```

- 鼠标按住状态可被实时轮询；录制/回放所需的边沿仍经过 pending/ready。

</details>

<details>
<summary><code>物理 Trigger 事件</code> — Godot 物理结果到 OnTouchStart 协程</summary>

```text
Godot PhysicsServer
`-- C++ SpxSprite/SpxPhysicsMgr trigger callback
    `-- func_on_trigger_enter(srcID, dstID)
        `-- gdengine.onTriggerEnter
            +-- GetSprite(srcID/dstID)
            `-- engine.Sprite.OnTriggerEnter
                `-- triggerEvents.pending 追加事件
                    `-- 下一次 internal/engine.onUpdate
                        +-- cacheTriggerEvents: pending -> ready
                        +-- Game.OnEngineUpdate
                        +-- gco.Update
                        `-- Game.OnEngineRender
                            `-- processPhysicsTriggers
                                +-- GetTriggerEvents
                                +-- 校验对象仍有效
                                `-- fireTouchStart
                                    `-- 创建 OnTouchStart Thread
                                        `-- 通常下一帧 gco.Update 运行
```

- Trigger 回调先由 ID 找到 Go Godot 代理，再由代理记录待处理事件。
- 触发处理位于 `Game.OnEngineRender()`，晚于本帧 `gco.Update()`。

</details>

<details>
<summary><code>OnCond 条件事件</code> — 不是外部回调，而是每帧采样</summary>

```text
Game.OnEngineBeforeUpdate
`-- scriptEvents.sampleConditions
    +-- 读取稳定输入和当前业务状态
    `-- 保存本帧上升沿命中 sink
        `-- Game.OnEngineUpdate
            `-- dispatchConditions
                `-- 创建条件处理器 Thread
                    `-- 同帧 gco.Update 运行首段
```

- 必须等 `StartDispatched` 后才采样。
- 条件只在 BeforeUpdate 求值一次，OnEngineUpdate 不重复求值。

</details>

<details>
<summary><code>AtFrame 回调</code> — 绝对帧队列创建新协程</summary>

```text
AtFrame(targetFrame, fn)
`-- engine.ScheduleFrame
    +-- 已到期：立即进入 executeFrameCallback
    `-- 未到期：frameRuntime.callbacks
        `-- runFrameScripts
            `-- RunFrameCallbacks
                `-- takeDue(CurrentFrame)
                    `-- gco.Create(callback owner, fn)
                        `-- gco.Update 运行
```

- `AtFrame` 创建新 Thread。
- 它与恢复原 Thread 的 `WaitNextFrame` 是两套机制。

</details>

<details>
<summary><code>Wait/WaitNextFrame</code> — 原协程挂起后由 gco.Update 恢复</summary>

```text
当前脚本 Thread
`-- Wait / WaitNextFrame / WaitYield
    +-- 标记当前 Thread blocked
    +-- 创建 WaitJob
    +-- Yield 释放 runMu
    `-- 后续 gco.Update 判断任务到期
        `-- markRunnableAndResume(Thread)
            +-- 先发布 runnable
            `-- 再唤醒底层 Go goroutine
                `-- 重新竞争 runMu 后从 Wait 调用点继续
```

- 恢复的是原 Thread，不会创建一个新的业务协程。
- `WaitNextFrame` 至少跨过一个逻辑帧。

</details>

</details>

---

## 最关键的边界

<details open>
<summary><code>runnable</code> 不等于“正在执行”</summary>

```text
Thread 已登记 runnable
    +-- 可能正在执行并持有 runMu
    +-- 可能等待 runMu
    `-- 可能底层 goroutine 尚未获得 CPU
```

`gco.Update()` 看到 runnable 后会等待其 Yield、阻塞或结束，而不是亲自调用其函数。

</details>

<details open>
<summary><code>gco.Update()</code> 等待执行片段，不等待完整协程生命周期</summary>

- Thread 调用 `WaitNextFrame()` 后变为 blocked，不再阻止当前 Update 返回。
- Thread 未来恢复后会继续执行后续片段。
- 无 Yield 的长循环会长期持有 `runMu`，协作式调度无法在任意语句处强制暂停它。

</details>

<details open>
<summary>一帧有两轮 Go -> Godot 视觉同步</summary>

```text
Game.OnEngineUpdate
`-- updateSpriteProxies              // gco.Update 前第一轮

gco.Update                           // 用户脚本修改视觉状态

Game.OnEngineRender
`-- syncPostCoroutineVisuals         // 绘制前第二轮补交
```

第二轮用于避免用户脚本在 `gco.Update()` 中产生的视觉变化延迟一帧。

</details>

<details open>
<summary>三个启动状态不能混为一谈</summary>

| 状态 | 置位点 | 含义 |
| --- | --- | --- |
| `IsRunned` | `markGameStarted()` | 项目资源与初始代理已经建立，可以进入正常帧逻辑 |
| `BootstrapDone` | `completeBootstrap()` | MainEntry、碰撞、Awake、精灵 Main 首段和 OnLoaded 已处理 |
| `StartDispatched` | `markStartDispatched()` | 用户 OnStart 已取得快照并完成派发阶段 |

`StartDispatched` 不表示所有 OnStart 已永久结束；`BatchWaitFirstSlice` 只保证它们执行到首次让出或结束。

</details>
