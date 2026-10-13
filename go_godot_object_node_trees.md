# Go 业务对象与 Godot 节点树对应关系

这份文档记录 SPX 中“Go 业务对象如何落到 Godot 场景树”的对应关系。重点是区分四种东西：

1. **Go 业务对象**：脚本真正持有的对象，例如 `*SpriteImpl`、`*ui.UiQuote`、`textBubble`。
2. **Go 引擎代理或句柄**：负责保存 Godot 对象 ID，并把方法转发给 Manager，例如 `engine.Sprite`、`gdx.UiNode`、`engine.Object`。
3. **Godot C++ 包装器/状态对象**：例如 `SpxSprite`、`SpxUi`、`SpxAudio`。它们由 SPX Manager 索引，未必是 `Node`。
4. **真正的 Godot 节点**：实际进入 `SceneTree`、参与渲染、输入、物理和生命周期通知的 `Node` 子类。

因此，表中的“Godot 对象”不能一律理解成一个节点。有些对象是 C++ 堆上的 Manager 或包装器，有些只是一个整数 ID，最后才由 Manager 找到场景树中的节点。

## 1. 总体场景树

`Spx::on_start()` 创建 `SpxEngineNode`，并把它作为 SPX 场景锚点；后续大多数 SPX 节点都挂在这里。`SpxCallbackProxy` 是一个例外，它直接挂在 Godot 的根 `Window` 下，用于延迟回调。

```text
SceneTree
└── Window (Godot 根窗口)
    ├── 用户场景节点/当前场景
    ├── SpxEngineNode                         (= SpxEngine::spx_root)
    │   ├── input_proxy                       (SpxInputProxy)
    │   ├── audio_root                        (默认音频播放器父节点)
    │   ├── dont_destroy_root                 (保活/特殊精灵节点)
    │   ├── sprite_root                       (普通运行时精灵父节点)
    │   │   └── SpxSprite ...                 (CharacterBody2D)
    │   ├── SpxUiMgr                          (CanvasLayer)
    │   │   └── Control 场景实例 ...
    │   ├── pure_sprite_root                  (轻量场景精灵父节点)
    │   │   ├── SpxRenderSprite ...           (Sprite2D)
    │   │   └── SpxStaticSprite ...           (StaticBody2D)
    │   ├── SpxCamera2D                       (仅在场景没有 Camera2D 时创建)
    │   ├── debug_root                        (调试绘制父节点)
    │   ├── pen_root                          (SpxPenSurface)
    │   │   ├── pen_render_target              (SubViewport)
    │   │   │   └── pen_canvas_drawer          (SpxPenCanvas)
    │   │   └── pen_canvas                    (Sprite2D，显示 SubViewportTexture)
    │   ├── SpxDrawTiles ...                  (瓦片编辑模式才创建)
    │   │   └── TileMapLayer ...
    │   └── TileMapLayer ...                  (JSON 运行时加载的地图层)
    └── SpxCallbackProxy                     (直接挂在 Window 根下)
```

上图展示的是典型结构，不代表每个节点在每个项目中都存在：

- `SpxEngineNode` 的子节点由各 Manager 的 `on_awake()` 按需创建。
- `SpxCamera2D` 只有找不到用户提供的 `Camera2D` 时才出现；用户相机可以位于当前场景任意后代节点。
- `SpxDrawTiles`、`PathDebugDrawer`、调试 `Line2D` 等属于工具或调试节点，可能不存在。
- 资源对象如 `TileSet`、`Texture2D`、`SpriteFrames` 由 Godot 引用计数管理，不是节点树分支。

## 2. 对应关系总表

| Go 业务对象 | Go 代理/句柄 | C++ 对象 | Godot 节点树位置 | 对象模型 |
| --- | --- | --- | --- | --- |
| `*SpriteImpl` 及生成的具体精灵类型 | `internal/engine.Sprite`，实现 `gdx.ISpriter`；ID 存在 `internal/engine.state.sprites` | `SpxSprite`，由 `SpxSpriteMgr` 的 `id_objects` 索引 | `SpxEngineNode/sprite_root/SpxSprite` | 完整三层：业务对象 -> Go 代理 -> C++/Godot 节点 |
| `textBubble`、`quoterBubble` | `*ui.UiSay`、`*ui.UiQuote` | `SpxUi` + `SpxUiBinding` | `SpxEngineNode/SpxUiMgr(CanvasLayer)/Ui*.tscn` 的 Control；绑定控件下面有隐藏的 `_spx_ui_binding` | Go 业务对象包 UI 代理，UI 代理再对应 C++ 包装器 |
| `*ui.UiAsk`、`*ui.UiDebug`、`*ui.UiMeasure`、`*ui.UiMonitor`、`*ui.UiSay`、`*ui.UiQuote` 及绑定的 `UiNode` | `gdx.UiNode`；ID 存在 `internal/engine.state.uiNodes` | `SpxUi`，本身不是 Node | `SpxUiMgr` 的 `CanvasLayer` 下的 Control；绑定子控件仍属于原来的 `.tscn` 树 | UI 专用两层代理 + Godot Control；`SpxUi` 不是场景节点 |
| `penComponent` | `engine.Object` 形式的 pen ID，命令进入 `PenSyncBuffer` | `SpxPen`（状态对象）和共享 `SpxPenSurface` | `SpxEngineNode/pen_root`；真正绘制在子 `SubViewport`，结果由兄弟 `pen_canvas` Sprite2D 显示 | 句柄式；多个 Go 笔对象共享一套画布 |
| `soundComponent` | `engine.Object` 声音 ID | `SpxAudio`（状态/声音管理对象） | `audio_root` 默认承载 `AudioStreamPlayer2D`；有 owner 时播放器挂到对应 `SpxSprite` 或 `Camera2D` | 句柄式；每个播放声音才创建一个播放器节点 |
| `gameTilemapMgr` / Go Tilemap API | 没有每张地图的 Go 引擎代理，主要保存 Go 侧地图状态 | `SpxDrawTiles` 或 `SpxTilemapparserMgr` | 编辑器：`SpxEngineNode/SpxDrawTiles/TileMapLayer`；运行时 JSON：`SpxEngineNode/TileMapLayer` | Manager + 节点集合；不是 `state.sprites` 式对象 |
| Go 轻量/静态场景精灵 API | 通常只有返回给排序、销毁接口使用的 ID | `SpxRenderSprite` / `SpxStaticSprite`，由 `SpxSceneMgr.id_pure_sprites` 索引 | `SpxEngineNode/pure_sprite_root` 下；静态精灵内部带 Sprite2D 和 CollisionShape2D | 句柄式；没有 `SpriteImpl` 和 `state.sprites` 条目 |
| `cameraImpl` / Go Camera API | 不保存一个 Godot 节点 ID，直接调用 `CameraMgr` | `SpxCameraMgr`；内部借用一个 `Camera2D *` | 优先复用用户场景中的 `Camera2D`；缺失时创建 `SpxEngineNode/SpxCamera2D` | 服务式；相机通常是全局唯一 |
| Go 输入状态/输入事件循环 | 通过 `InputMgr` 查询，事件由回调进入 Go 缓存 | `SpxInputMgr` + `SpxInputProxy` | `SpxEngineNode/input_proxy` | 服务式；代理节点只负责接收 Godot 输入信号 |
| Go 物理查询/同步状态 | `SpriteSyncBuffer`、物理查询接口 | `SpxPhysicsMgr` + Godot PhysicsServer/物理节点 | 没有独立物理根节点；使用 `SpxSprite`、`Area2D`、`CollisionShape2D` 等已有节点 | 服务式；物理节点归精灵树所有 |
| Go 寻路状态 | Navigation API 返回的路径/查询结果 | `SpxNavigationMgr` + 引用计数的 `SpxPathFinder` | 默认无节点；启用调试时在 `SpxEngineNode` 下挂 `PathDebugDrawer` | 服务式；调试绘制才产生节点 |
| Go 资源、平台、扩展、调试调用 | 接口或 Manager 包装，不保存业务节点 | `SpxResMgr`、`SpxPlatformMgr`、`SpxExtMgr`、`SpxDebugMgr` | 资源/平台/扩展无固定节点；调试绘制在 `debug_root` 下创建临时 Line2D/Node2D | 全局服务式 |

## 3. 普通运行时精灵

### 3.1 Go 侧对象链

```text
Go 业务层
└── *SpriteImpl（或用户生成的具体精灵类型）
    ├── runtimeState.SyncSprite -> *internal/engine.Sprite
    ├── physicsComponent / animationComponent / soundComponent / penComponent ...
    └── 通过 SpriteMgr、SyncBuffer 操作 Godot

Go 引擎代理层
└── internal/engine.Sprite
    └── gdx.Sprite / pkg/spx/pkg/engine.ISpriter
        └── Id = Godot 侧 SpxSprite 的 SPX gid
```

`internal/engine.state.sprites` 保存的是 `map[Object]gdx.ISpriter`，目的是接收 Godot 回调时按 gid 找回 Go 代理；它不是 Godot 节点的所有权容器。真正的节点由 `sprite_root` 和 SceneTree 持有。

### 3.2 Godot 节点树

```text
SpxEngineNode
└── sprite_root (Node2D)
    └── SpxSprite (CharacterBody2D)
        ├── RenderRoot (Node2D)
        │   ├── Anim2D (AnimatedSprite2D)
        │   └── VisibleNotifier2D (VisibleOnScreenNotifier2D)
        ├── Area2D
        │   └── Trigger2D (CollisionShape2D)
        └── Collider2D (CollisionShape2D)
```

`RenderRoot` 和 `Anim2D` 承担视觉变换；`Collider2D` 是实体碰撞；`Area2D/Trigger2D` 是感应区域。加载 `.tscn` 精灵时，Prefab 可以包含自己的组件，但 `SpxSprite::on_start()` 会按名称解析或补齐上述运行时组件。

### 3.3 轻量和静态场景精灵

```text
SpxEngineNode
└── pure_sprite_root (Node2D)
    ├── SpxRenderSprite (Sprite2D)
    └── SpxStaticSprite (StaticBody2D)
        ├── Sprite2D
        └── CollisionShape2D
```

它们由 `SpxSceneMgr` 直接创建，适合背景、装饰和静态碰撞。Go 侧没有对应的 `SpriteImpl`，也不会进入 `runtimeState.sprites`；销毁时通过 `id_pure_sprites` 找到 C++ 节点并 `queue_free()`。

## 4. UI、气泡和交互控件

### 4.1 Go 对象关系

```text
Go 游戏业务
├── textBubble
│   └── panel *ui.UiSay
├── quoterBubble
│   └── panel *ui.UiQuote
└── Game.dialogState.AskPanel
    └── *ui.UiAsk

internal/ui
├── UiSay / UiQuote / UiAsk / UiDebug / UiMeasure / UiMonitor ...
└── 都嵌入或组合 engine.UiNode
    └── engine.UiNode = gdx.UiNode（保存 ID 和事件入口）
```

UI 的 Go 对象既包含业务生命周期（`OnStart`、`OnUpdate`、`Destroy`），也包含对控件属性的代理调用。因此它不像普通精灵那样再拆出一个独立的 `internal/engine.UiQuote` 业务实现；底层统一使用 `gdx.UiNode` 和 `UiMgr`。

### 4.2 Godot 节点树

```text
SpxEngineNode
└── SpxUiMgr (CanvasLayer)
    ├── UiAsk.tscn (Control 根节点)
    │   ├── Frame / AskBody / Input / Check ...
    │   │   └── _spx_ui_binding (SpxUiBinding，内部节点)
    │   └── 其他场景子控件
    ├── UiQuote.tscn (Control 根节点)
    │   ├── ImageL / ImageR
    │   ├── LabelDes / LabelMsg
    │   └── 各绑定控件的 _spx_ui_binding
    ├── UiSay.tscn (Control 根节点)
    └── UiDebug.tscn / UiMeasure.tscn / UiMonitor.tscn ...
```

每个被 `UiMgr.CreateNode` 或 `BindNode` 暴露给 Go 的 Control 都对应一个 C++ `SpxUi` 包装器和一个 `SpxUiBinding` 子节点，但 `SpxUi` 自身不在 SceneTree 中。`SpxUiBinding` 监听按钮、输入等 Godot 信号，再由 `SpxUiMgr` 回调 Go 的 UI ID。

## 5. 画笔（Pen）

`penComponent` 只有一个 `penObj` 句柄；不同精灵的落笔、盖章命令最终汇聚到共享画布，而不是每个精灵创建自己的 Viewport。

```text
SpxEngineNode
└── pen_root (SpxPenSurface / Node2D)
    ├── pen_render_target (SubViewport，透明离屏目标)
    │   └── pen_canvas_drawer (SpxPenCanvas / Node2D)
    │       └── RenderingServer CanvasItem 绘制直线、印章
    └── pen_canvas (Sprite2D)
        └── texture = pen_render_target.get_texture()
```

因此，`pen_canvas` 只是把 `SubViewportTexture` 显示到主画布；真正的画线发生在 `pen_canvas_drawer` 所属的 SubViewport 中。

## 6. 音频

```text
SpxEngineNode
└── audio_root (Node2D，由 SpxAudioMgr 创建)
    └── AudioStreamPlayer2D ...               (无 owner 时的默认父节点)

SpxSprite (owner_id 指向精灵时)
└── AudioStreamPlayer2D ...                   (随精灵位置产生 2D 衰减)

Camera2D (owner_id = -1 时)
└── AudioStreamPlayer2D ...                   (以相机为声源)
```

Go 的 `soundComponent` 保存的是声音 ID，不保存 `AudioStreamPlayer2D` 指针。一个声音播放实例才会在 Godot 侧创建一个播放器，停止或播放结束后由 `SpxAudio` 回收。

## 7. TileMap 和场景辅助精灵

### 7.1 编辑器/绘制模式

```text
SpxEngineNode
└── SpxDrawTiles (Node2D)
    ├── __spx_tile_layer_0 (TileMapLayer)
    ├── __spx_tile_layer_1 (TileMapLayer)
    └── ...
```

`SpxTilemapMgr` 保存 `SpxDrawTiles *`，`SpxDrawTiles` 再保存图层索引到 `TileMapLayer *` 的映射。共享的 `TileSet`、图集纹理和碰撞数据是资源对象，不是额外节点。

### 7.2 JSON 运行时加载

```text
SpxEngineNode
├── TileMapLayer (地图 A 的 layer 0)
├── TileMapLayer (地图 A 的 layer 1)
└── TileMapLayer (地图 B 的 layer 0)
```

`SpxTilemapparserMgr::load_tilemap()` 先把 JSON 解析为 `SpxTileMapData`，再创建共享 `TileSet` 和多个 `TileMapLayer`，最后直接把每层挂到 `SpxEngineNode`。Manager 用“地图名 -> `Vector<TileMapLayer *>`”保存索引，Go 侧没有一一对应的 TileMap 节点代理。

## 8. 相机、输入和服务型 Manager

### 相机

```text
情况 A：项目已有相机
Window
└── 用户场景 ...
    └── Camera2D（SpxCameraMgr 复用，位置由用户场景决定）

情况 B：项目没有相机
SpxEngineNode
└── SpxCamera2D (Camera2D，由 SpxCameraMgr 创建)
```

`cameraImpl` 是 Go 业务侧的门面，真正的 Camera2D 由 `SpxCameraMgr` 统一持有借用指针；它不进入 Go 精灵注册表。

### 输入

```text
SpxEngineNode
└── input_proxy (SpxInputProxy)
```

`SpxInputProxy` 接收 Godot Viewport 的键鼠事件，转换成 SPX ABI 回调；Go 的输入循环再把回调放入自己的事件状态。键盘状态查询仍直接访问 Godot `Input` 单例，没有额外节点。

### 物理

没有单独的 `physics_root`。`SpxPhysicsMgr` 使用 Godot 物理服务器，并操作精灵树中的 `CharacterBody2D`、`Area2D`、`CollisionShape2D`。因此物理是“服务 + 既有节点”，而不是一棵独立节点树。

### 寻路

默认情况下 `SpxPathFinder` 是 `RefCounted` 数据对象，不进入树；开启调试绘制时才追加：

```text
SpxEngineNode
└── PathDebugDrawer (Node2D，可选)
```

### 调试

```text
SpxEngineNode
└── debug_root (Node2D，z_index = 1000)
    ├── Line2D / Node2D（显示一帧的调试图形）
    └── ...
```

`SpxDebugMgr` 每帧清理上一帧的调试节点；它们不是 Go 业务对象，也不会进入精灵或 UI 注册表。

资源、平台、扩展 Manager 只操作 `ResourceLoader`、`DisplayServer`、扩展接口和回调表，没有固定的 SceneTree 节点。

## 9. 记忆方法：谁拥有谁

```text
Go state.sprites / state.uiNodes
    只保存“ID -> Go 代理”，用于生命周期和回调分发

SpxEngine 各 Manager 的 map
    保存“ID -> C++ 包装器/借用节点”，用于 Godot 主线程操作

Godot SceneTree
    真正拥有 Node；add_child 后由父节点管理，销毁通常 queue_free

Godot Resource 引用计数
    拥有 Texture2D、TileSet、SpriteFrames、AudioStream 等资源
```

判断一个 Go 对象对应哪种 Godot 结构时，可以按这个顺序问：

1. Go 对象是否需要自己的生命周期和事件？需要时通常有 Go 代理（普通精灵、UI）。
2. Godot 是否需要持续参与渲染、输入或物理？需要时必须有 SceneTree Node。
3. 是否只是一次查询、播放或绘制命令？通常只需要 Manager + ID 句柄（音频、画笔、静态场景精灵）。
4. 是否是全局能力？通常是 Manager 服务，不会创建一棵专属节点树（资源、平台、物理、相机复用）。

## 10. 关键源码入口

- [SPX 启动与 `SpxEngineNode`](godot_modules/spx/spx.cpp)、[`SpxEngine`](godot_modules/spx/spx_engine.cpp)
- [普通精灵创建与组件树](godot_modules/spx/spx_sprite_mgr.cpp)、[`SpxSprite` 组件解析](godot_modules/spx/spx_sprite.cpp)
- [轻量/静态场景精灵](godot_modules/spx/spx_scene_mgr.cpp)
- [UI CanvasLayer、Control 与绑定包装器](godot_modules/spx/spx_ui_mgr.cpp)、[`SpxUi`](godot_modules/spx/spx_ui.cpp)
- [画笔 SubViewport 与显示 Sprite2D](godot_modules/spx/spx_pen_surface.cpp)、[`SpxPenMgr`](godot_modules/spx/spx_pen_mgr.cpp)
- [音频播放器节点](godot_modules/spx/spx_audio.cpp)、[`SpxAudioMgr`](godot_modules/spx/spx_audio_mgr.cpp)
- [TileMap 编辑器层](godot_modules/spx/spx_tilemap_mgr.cpp)、[`SpxDrawTiles`](godot_modules/spx/spx_draw_tiles.cpp)
- [JSON TileMap 运行时层](godot_modules/spx/spx_tilemapparser_mgr.cpp)
- [Go UI 创建与代理注册](internal/engine/runtime_factory.go)、[Go UI 类型](internal/ui)
- [Go 精灵代理与接口](internal/engine/sprite.go)、[公共 `ISpriter`/`IUiNode`](pkg/spx/pkg/engine/interface.go)

