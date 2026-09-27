# SPX 游戏项目目录结构与 `index.json` 配置说明

本文介绍一个典型 SPX 游戏项目的目录结构、文件命名规则、资源路径规则，以及各种 `index.json` 的用途和字段含义。示例以当前 SPX v3 运行时为准。

> JSON 标准不支持注释。本文将说明写在表格和正文中，代码块中的 JSON 可以直接复制使用。不要在实际 `index.json` 中加入 `//` 或 `#` 注释。

## 1. 最小可运行项目

使用 `spx init` 创建的独立最小项目通常包含入口脚本、模块文件和项目资源配置：

```text
MyGame/
├── main.spx
├── go.mod
└── assets/
    └── index.json
```

一个常见的最小配置如下：

```json
{
  "map": {
    "width": 480,
    "height": 360
  },
  "zorder": []
}
```

建议显式填写 `map.width`、`map.height` 和 `zorder`。这样即使项目暂时没有背景或角色，舞台尺寸和图层列表仍然是确定的。

## 2. 典型项目目录结构

```text
MyGame/
├── main.spx                         # 游戏/舞台入口脚本
├── Player.spx                       # Player 角色行为脚本
├── Enemy.spx                        # Enemy 角色行为脚本
├── go.mod                           # Go 模块和 SPX 依赖，通常由 spx init 创建
├── go.sum                           # Go 依赖校验文件，由工具生成
│
├── assets/                          # 项目的全部运行时资源
│   ├── index.json                   # 项目级配置：舞台、背景、图层和运行参数
│   ├── index_pack.json              # 聚合配置，通常由打包工具生成
│   ├── background.png               # 背景资源，可放在 assets 的子目录中
│   ├── music.ogg                    # 直接通过路径引用的背景音乐
│   │
│   ├── sprites/                     # 角色资源根目录
│   │   ├── Player/                  # 名称与 Player.spx、zorder 中的 Player 对应
│   │   │   ├── index.json           # Player 的初始状态、造型、动画和物理配置
│   │   │   ├── idle.png             # 角色造型
│   │   │   ├── walk-1.png
│   │   │   └── walk-2.png
│   │   └── Enemy/
│   │       ├── index.json
│   │       └── enemy.svg
│   │
│   ├── sounds/                      # 按名称加载的声音资源
│   │   ├── jump/
│   │   │   ├── index.json           # jump 声音元数据
│   │   │   └── jump.wav
│   │   └── hit/
│   │       ├── index.json
│   │       └── hit.wav
│   │
│   ├── fonts/                       # 可选：项目字体集合
│   │   └── Game Font/
│   │       ├── index.json
│   │       └── game-font.ttf
│   │
│   └── tilemaps/                    # 可选：TileMap 配置和贴图资源
│       └── level-1.json
│
├── project/                         # 自动生成的 Godot 工程
├── .temp/                           # 临时运行文件
└── xgo_autogen.go                   # XGo 生成的 Go 代码，可能在构建后出现
```

### 2.1 应该手工维护的文件

- `main.spx`：游戏入口和舞台级逻辑。
- `<角色名>.spx`：对应角色的行为代码。
- `assets/index.json`：项目级源配置。
- `assets/sprites/<角色名>/index.json`：角色资源配置。
- `assets/sounds/<声音名>/index.json`：声音资源配置。
- 图片、音频、字体和 TileMap 等原始资源。
- 独立项目根目录下的 `go.mod`。

### 2.2 不应直接修改的生成内容

- `project/`：SPX 命令生成的 Godot 运行工程。
- `project/go/main.go`：由 XGo 根据 `.spx` 源码生成。
- `.temp/`：临时运行环境。
- `xgo_autogen.go`：XGo 中间生成代码。
- `go.sum`：依赖校验数据，由 Go 工具维护。

`assets/index_pack.json` 是一个特殊情况：它是供打包/运行时使用的聚合配置，通常应由工具重新生成，而不是与各个源 `index.json` 手工双写。

## 3. 名称对应规则

角色名称会同时出现在脚本、目录和项目配置中：

```text
Player.spx
    ↕ 同名
assets/sprites/Player/index.json
    ↕ 同名
assets/index.json 中 zorder 的 "Player"
```

需要遵守以下规则：

1. `Player.spx` 的文件名去掉 `.spx` 后就是角色名 `Player`。
2. 角色资源目录使用 `assets/sprites/Player/`。
3. 普通角色必须在 `zorder` 中以字符串 `"Player"` 注册，或者通过 `type: "sprite"`/`"sprites"` 条目注册。
4. 名称应保持完全一致，包括大小写。大小写不一致可能在 macOS 上暂时可用，却在 Linux、Web 或打包环境中失败。
5. `main.spx` 是保留的项目入口名称，不对应 `assets/sprites/main/`。

例如 `01-Weather` 项目有如下对应关系：

| 行为脚本 | 角色资源目录 | `zorder` 名称 |
| --- | --- | --- |
| `Jaime.spx` | `assets/sprites/Jaime/` | `"Jaime"` |
| `Kai.spx` | `assets/sprites/Kai/` | `"Kai"` |

## 4. 资源路径规则

不同配置文件中的相对路径采用不同基准：

| 配置位置 | `path` 的相对基准 | 示例 | 最终资源 |
| --- | --- | --- | --- |
| `assets/index.json` | `assets/` | `"background.png"` | `assets/background.png` |
| `assets/index.json` | `assets/` | `"img/city.png"` | `assets/img/city.png` |
| `assets/sprites/Player/index.json` | `assets/sprites/Player/` | `"idle.png"` | `assets/sprites/Player/idle.png` |
| `assets/sounds/jump/index.json` | `assets/sounds/jump/` | `"jump.wav"` | `assets/sounds/jump/jump.wav` |
| `assets/fonts/Game Font/index.json` | 当前字体目录 | `"game-font.ttf"` | `assets/fonts/Game Font/game-font.ttf` |

路径建议统一使用 `/`，不要在 JSON 中使用 Windows 的 `\`。普通项目资源尽量使用相对路径，以便桌面、Web 和导出包使用同一套配置。

## 5. 坐标与数值约定

SPX 舞台坐标与图片内部坐标不同：

- 舞台原点位于中心。
- 舞台 X 轴向右为正，Y 轴向上为正。
- 角色配置最外层的 `x`、`y` 是舞台坐标。
- 造型对象内部的 `x`、`y` 是图片锚点，原点位于图片左上角，Y 轴向下。
- `heading` 使用角度；`90` 通常表示向右，`0` 表示向上。
- `size` 是缩放倍数；`1` 是原始逻辑大小，`0.5` 是一半大小。
- `bitmapResolution` 是位图像素密度；逻辑尺寸等于原始像素尺寸除以该值。
- `backdropIndex` 和 `costumeIndex` 都从 `0` 开始。

## 6. 项目级 `assets/index.json`

项目级 `index.json` 控制整个舞台。下面是一份较完整但仍适合普通项目的示例：

```json
{
  "run": {
    "title": "My Game",
    "width": 480,
    "height": 360,
    "fullScreen": false,
    "pauseOnUnfocused": false,
    "eventQueuePolicy": "drop_oldest"
  },
  "map": {
    "width": 960,
    "height": 720,
    "mode": "fillRatio"
  },
  "backdrops": [
    {
      "name": "city",
      "path": "background.png",
      "bitmapResolution": 1,
      "x": 240,
      "y": 180
    }
  ],
  "backdropIndex": 0,
  "stretchMode": true,
  "windowScale": 1,
  "maxFPS": 60,
  "camera": {
    "on": "Player"
  },
  "bgm": "music.ogg",
  "fontPreferences": ["Game Font", "default"],
  "zorder": [
    "Player",
    "Enemy"
  ]
}
```

### 6.1 常用顶层字段

| 字段 | 类型 | 默认/省略行为 | 说明 |
| --- | --- | --- | --- |
| `run` | object | 使用运行时默认值 | 窗口标题、窗口尺寸、全屏和事件队列策略。 |
| `map` | object | 取决于背景或 TileMap | 游戏世界尺寸和背景填充模式。建议显式配置。 |
| `backdrops` | array | 无背景 | 舞台背景列表。每一项使用背景对象格式。 |
| `backdropIndex` | integer | `0` | 启动时显示的背景下标，从 `0` 开始；越界时运行时回退到 `0`。 |
| `zorder` | array | 空列表 | 舞台对象及绘制顺序。列表后面的普通角色通常位于前面角色的上层。 |
| `stretchMode` | boolean | `true` | 是否按运行窗口/内容区域进行拉伸。 |
| `windowScale` | number | `1` | 桌面窗口缩放倍数；小于 `0.001` 时回退为 `1`。 |
| `maxFPS` | integer | 平台默认行为 | 交给运行平台设置最大帧率；普通项目常用 `60`。 |
| `camera` | object | 不主动跟随角色 | 相机的初始配置。 |
| `debug` | boolean | `false` | 是否启用 SPX 调试模式及调试日志。 |
| `bgm` | string | 不播放 BGM | 启动时播放的背景音乐路径，相对于 `assets/`。 |
| `fontPreferences` | string[] | `["default"]` | 项目字体回退顺序。显式空数组表示不使用全局字体。 |
| `tilemapPath` | string | 不加载 TileMap | TileMap 配置路径，相对于 `assets/`。 |

### 6.2 `run` 运行参数

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `title` | string | 窗口标题。省略时根据当前项目目录生成标题。 |
| `width` | integer | 运行窗口的逻辑宽度。为 `0` 时通常由当前背景/世界尺寸决定。 |
| `height` | integer | 运行窗口的逻辑高度。为 `0` 时通常由当前背景/世界尺寸决定。 |
| `fullScreen` | boolean | 请求全屏运行。顶层兼容字段 `fullscreen` 也会参与最终全屏判断。 |
| `pauseOnUnfocused` | boolean | 窗口失去焦点时是否暂停；省略时允许失焦继续运行。 |
| `screenshotKey` | string | 引擎原生截图快捷键；未配置时可由 `SPX_SCREENSHOT_KEY` 环境变量提供。 |
| `eventQueuePolicy` | string | 事件队列满时的策略，见下表。 |
| `keyDuration` | integer | 配置结构保留的按键时长字段；普通项目通常无需设置。 |

`eventQueuePolicy` 支持：

| 值 | 行为 |
| --- | --- |
| `drop_newest` 或未识别值 | 队列满时丢弃新事件，也是默认策略。 |
| `drop_oldest`、`dropoldest`、`DropOldest` | 队列满时移除最旧事件，再加入新事件。 |
| `block`、`Block` | 在允许阻塞的调用路径中等待队列空间。 |

### 6.3 `map` 世界与背景模式

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `width` | integer | 游戏世界宽度，单位为逻辑像素。 |
| `height` | integer | 游戏世界高度，单位为逻辑像素。 |
| `mode` | string | 背景相对于世界区域的布局方式。 |

`map.mode` 支持：

| 值 | 说明 |
| --- | --- |
| `""` 或 `fill` | 拉伸到目标区域；未知值也会回退到此模式。 |
| `repeat` | 平铺背景。 |
| `fillRatio` | 保持宽高比并覆盖目标区域；超出目标区域的部分可能被裁切。 |
| `fillCut` | 保持宽高比并让图片完整落入目标区域；宽高比不同时可能留边。 |
| `actualSize` | 使用背景图片的实际逻辑尺寸。 |

`map.width`/`height` 表示游戏世界大小，`run.width`/`height` 表示窗口大小。世界可以大于窗口，此时相机只显示世界的一部分。

### 6.4 `backdrops` 背景对象

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | 背景名称，脚本切换背景时可以使用。 |
| `path` | string | 图片路径，相对于 `assets/`。 |
| `bitmapResolution` | integer | 位图密度，常用 `1` 或 `2`；省略/无效时按 `1` 处理。 |
| `x` | number | 背景图片锚点的 X 坐标，使用图片内部坐标。 |
| `y` | number | 背景图片锚点的 Y 坐标，使用图片内部坐标。 |
| `imageWidth` | number | 可选的原始图片宽度元数据。 |
| `imageHeight` | number | 可选的原始图片高度元数据。 |
| `faceRight` | number | 图片天然朝向相对 SPX 朝向的角度补偿。背景通常不需要设置。 |
| `pivot` | object | 可选的额外锚点偏移，形如 `{"x": 0, "y": 0}`。 |

如果背景没有特殊锚点需求，可以只写 `name` 和 `path`。

### 6.5 `camera`

```json
{
  "camera": {
    "on": "Player"
  }
}
```

`camera.on` 是启动后相机跟随的角色名。该名称必须能解析为项目中的角色。

### 6.6 显示和图层字段

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `stretchMode` | boolean | `true` | 是否让显示内容适配窗口。 |
| `windowScale` | number | `1` | 桌面窗口的初始缩放比例。Web/全屏环境会根据宿主窗口重新计算合适比例。 |
| `maxFPS` | integer | 平台默认行为 | 最大帧率。 |
| `fullscreen` | boolean | `false` | 顶层全屏兼容开关。 |
| `layerSortMode` | string | 空/固定 Z 序 | `vertical` 时启用基于垂直位置的排序；其他值使用普通图层顺序。 |

## 7. `zorder` 图层与舞台对象

`zorder` 不只是角色名数组。每一项可以是：

- 字符串：普通角色名称。
- `type: "sprite"`：注册一个角色，并覆盖部分初始属性。
- `type: "sprites"`：从同一种角色原型创建一组舞台实例。
- `type: "monitor"` 或 `"stageMonitor"`：变量监视器。
- `type: "measure"`：舞台测量标记。

数组按顺序分配图层。通常越靠后的角色绘制层越高。

### 7.1 普通角色名称

```json
{
  "zorder": ["BackgroundObject", "Player", "ForegroundObject"]
}
```

每个名称需要同时存在对应的 `<名称>.spx` 和 `assets/sprites/<名称>/index.json`。

### 7.2 单个 `sprite` 条目

```json
{
  "type": "sprite",
  "target": "Player",
  "x": -120,
  "y": 40,
  "heading": 90,
  "rotationStyle": "leftRight",
  "visible": true,
  "size": 0.8,
  "costumeIndex": 1
}
```

| 字段 | 必需 | 说明 |
| --- | --- | --- |
| `type` | 是 | 固定为 `"sprite"`。 |
| `target` | 是 | 对应的角色名称。 |
| `x`、`y` | 否 | 覆盖角色资源配置中的初始位置。 |
| `heading` | 否 | 覆盖初始朝向。 |
| `rotationStyle` | 否 | 覆盖旋转方式。 |
| `visible` | 否 | 覆盖初始可见性。 |
| `size` | 否 | 覆盖初始缩放。 |
| `costumeIndex` | 否 | 覆盖初始造型下标，必须在造型范围内。 |

### 7.3 一组 `sprites` 条目

```json
{
  "type": "sprites",
  "target": "Coins",
  "items": [
    {"x": -100, "y": 0},
    {"x": 0, "y": 0, "costumeIndex": 1},
    {"x": 100, "y": 0}
  ]
}
```

`target` 对应 `main.spx` 中的角色数组/切片字段；`items` 中每个对象支持与 `sprite` 相同的属性覆盖字段。所有实例复用同一种角色脚本和资源配置，但拥有独立运行状态。

### 7.4 `monitor` 变量监视器

```json
{
  "type": "monitor",
  "name": "score-monitor",
  "target": "Player",
  "val": "getVar:score",
  "label": "Score",
  "mode": "slider",
  "sliderMin": 0,
  "sliderMax": 100,
  "isDiscrete": true,
  "x": 10,
  "y": 10,
  "visible": true,
  "color": "#289cfc"
}
```

| 字段 | 必需 | 说明 |
| --- | --- | --- |
| `type` | 是 | `"monitor"` 和 `"stageMonitor"` 都可用。 |
| `name` | 是 | 监视器自身的唯一名称。 |
| `target` | 是 | 变量所属角色；空字符串表示舞台/游戏级变量。 |
| `val` | 是 | 读取方式；常见格式为 `getVar:<变量名>`。列表监视器也可以直接使用变量名。 |
| `label` | 是 | 显示标签；不需要文字时可以使用空字符串。 |
| `mode` | 是 | 显示模式：`1` 普通、`2` 大号、`3`/`"slider"` 滑块、`4`/`"list"` 列表。 |
| `style` | 否 | `"scratch"` 使用 Scratch 风格外观；省略为默认风格。 |
| `size` | 否 | 整个监视器的缩放倍数，默认 `1`。 |
| `x`、`y` | 是 | 监视器在舞台 UI 中的位置。 |
| `visible` | 是 | 是否显示。 |
| `color` | 否 | 数字颜色、颜色名称或 `#RRGGBB` 字符串。 |
| `sliderMin`、`sliderMax` | 否 | 滑块最小值和最大值，默认 `0` 和 `100`。若顺序写反，运行时会交换。 |
| `isDiscrete` | 否 | `true` 时步长为 `1`；`false` 时步长为 `0.01`。 |
| `width`、`height` | 否 | `list` 模式的尺寸；默认约为 `100 x 200`，并受最小尺寸约束。 |

### 7.5 `measure` 测量标记

```json
{
  "type": "measure",
  "x": 0,
  "y": 100,
  "heading": 90,
  "size": 10,
  "scale": 10,
  "color": "#ffffff"
}
```

`x`、`y`、`heading` 和 `size` 是必需的数值字段；`scale` 默认 `1`，`color` 默认黑色。它主要用于在舞台上显示带数值的距离/方向标记。

## 8. 项目级高级字段

### 8.1 碰撞与物理

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `physics` | boolean | `false` | 是否启用物理模式。关闭时保持类似 Scratch 的运动行为。 |
| `collisionByShape` | boolean | `false` | 非物理模式下使用形状碰撞；否则默认使用像素碰撞。 |
| `pixelCollisionPrecision` | string | `low` | 像素碰撞采样精度：`low`、`medium` 或 `high`。精度越高，计算成本越高。 |
| `autoSetCollisionLayer` | boolean | `true` | 是否自动分配碰撞层。当前配置要求它与 `physics` 的启用状态不同；物理项目通常显式写 `false`。 |
| `globalGravity` | number | `1` | 全局重力缩放系数。 |
| `globalFriction` | number | `1` | 全局摩擦缩放系数。 |
| `globalAirDrag` | number | `1` | 全局空气阻力缩放系数。 |

物理项目的常见顶层配置：

```json
{
  "physics": true,
  "autoSetCollisionLayer": false,
  "globalGravity": 1,
  "globalFriction": 1,
  "globalAirDrag": 1
}
```

### 8.2 寻路与空间音频

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `pathCellSizeX` | integer | `16` | 寻路网格单元宽度。 |
| `pathCellSizeY` | integer | `16` | 寻路网格单元高度。 |
| `audioAttenuation` | number | `0` | 空间音频衰减参数。 |
| `audioMaxDistance` | number | `2000` | 空间音频最大作用距离。 |

### 8.3 字体

项目字体目录：

```text
assets/fonts/Game Font/
├── index.json
└── game-font.ttf
```

字体 `index.json`：

```json
{
  "faces": [
    {"path": "game-font.ttf"}
  ]
}
```

在项目级 `assets/index.json` 中指定回退顺序：

```json
{
  "fontPreferences": ["Game Font", "default"]
}
```

- 每个 `assets/fonts/` 的直接子目录代表一个 Font Family，目录名就是 Family 名称。
- 当前每个 Family 只支持一个 Face。
- `default` 是内置保留名称，不能创建 `assets/fonts/default/` 覆盖它。
- `fontPreferences` 缺失或为 `null` 时使用 `["default"]`。
- 显式写 `[]` 表示不使用任何全局字体回退。

## 9. 角色级 `assets/sprites/<角色>/index.json`

角色配置负责造型、初始位置、动画以及物理属性。

```json
{
  "costumes": [
    {
      "name": "idle",
      "path": "idle.png",
      "bitmapResolution": 2,
      "x": 50,
      "y": 80
    },
    {
      "name": "walk-1",
      "path": "walk-1.png",
      "bitmapResolution": 2,
      "x": 50,
      "y": 80
    }
  ],
  "costumeIndex": 0,
  "heading": 90,
  "rotationStyle": "normal",
  "size": 1,
  "visible": true,
  "isDraggable": false,
  "x": 0,
  "y": 0,
  "fAnimations": {
    "walk": {
      "frameFrom": "idle",
      "frameTo": "walk-1",
      "frameFps": 12,
      "isLoop": true
    }
  }
}
```

### 9.1 角色初始状态字段

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `x`、`y` | number | 角色在舞台中的初始位置。 |
| `heading` | number | 初始朝向角度。 |
| `size` | number | 初始缩放倍数。 |
| `visible` | boolean | 启动时是否可见。 |
| `isDraggable` | boolean | 是否允许拖动。 |
| `rotationStyle` | string | `normal`：正常旋转；`leftRight`/`left-right`：只左右翻转；`none`：不旋转。未知值回退到 `normal`。 |
| `costumeIndex` | integer | 默认造型下标，从 `0` 开始。越界时运行时回退到 `0`。 |
| `pivot` | object | 角色逻辑原点的额外偏移，形如 `{"x": 0, "y": 0}`。 |
| `defaultAnimation` | string | 可选的默认动画名称。 |
| `animBindings` | object | 动画名称绑定表，用于把语义动作映射到具体动画。 |

### 9.2 `costumes` 造型数组

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | 造型名称，动画和 `setCostume` 可以按名称引用。 |
| `path` | string | 相对于当前角色目录的图片路径。 |
| `x`、`y` | number | 图片内部锚点，坐标从图片左上角开始。 |
| `imageWidth`、`imageHeight` | number | 可选的原始图片尺寸元数据。 |
| `faceRight` | number | 图片天然朝向的角度补偿。 |
| `bitmapResolution` | integer | 图片像素密度，通常为 `1` 或 `2`。 |

造型锚点通常设置在角色希望进行旋转、移动和碰撞定位的位置。例如人物角色常把锚点放在身体中心或脚底附近。

### 9.3 `costumeSet` 单图集

大量连续帧可以通过一个图集声明：

```json
{
  "costumeSet": {
    "path": "sheet.png",
    "imageWidth": 960,
    "imageHeight": 640,
    "bitmapResolution": 2,
    "faceRight": 90,
    "nx": 8,
    "rect": {"x": 0, "y": 0, "w": 960, "h": 640},
    "items": [
      {"namePrefix": "walk-", "n": 8}
    ]
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `path` | 图集图片路径。 |
| `imageWidth`、`imageHeight` | 图集原始尺寸元数据。 |
| `bitmapResolution` | 位图密度。 |
| `faceRight` | 图集中角色的天然朝向。 |
| `nx` | 每行帧数。 |
| `rect` | 使用的图集区域：`x`、`y`、`w`、`h`。 |
| `items` | 命名分组；`namePrefix` 是帧名前缀，`n` 是连续帧数量。 |

### 9.4 `costumeMPSet` 多区域图集

`costumeMPSet` 允许同一张图中存在多个帧区域：

```json
{
  "costumeMPSet": {
    "path": "sheet.png",
    "bitmapResolution": 1,
    "faceRight": 90,
    "parts": [
      {
        "rect": {"x": 0, "y": 0, "w": 400, "h": 100},
        "nx": 4,
        "items": [{"namePrefix": "walk-", "n": 4}]
      },
      {
        "rect": {"x": 0, "y": 100, "w": 200, "h": 100},
        "nx": 2,
        "items": [{"namePrefix": "jump-", "n": 2}]
      }
    ]
  }
}
```

每个 `parts` 项都有自己的 `nx`、`rect` 和 `items`，适合一张图片中帧区域不连续的情况。

### 9.5 动画字段

角色配置支持三个动画表：

| 字段 | 用途 |
| --- | --- |
| `fAnimations` | 帧动画及常用动作动画。项目示例主要使用此字段。 |
| `mAnimations` | 移动类动画配置表。 |
| `tAnimations` | 转向类动画配置表。 |

每个表都是 `动画名 -> 动画配置` 的对象。动画配置字段包括：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `frameFrom` | string/number | 起始造型名称或下标。 |
| `frameTo` | string/number | 结束造型名称或下标。 |
| `frameFps` | integer | 帧动画每秒帧数。 |
| `stepDuration` | number | 移动一步对应的动画时间。 |
| `turnToDuration` | number | 转向动画持续时间。 |
| `anitype` | integer | 内部动画类型：`0` 帧、`1` 移动、`2` 转向、`3` 滑行。普通项目优先使用对应动画表和高级 API。 |
| `isLoop` | boolean | 是否循环。 |
| `isKeepOnStop` | boolean | 停止动画后是否保持停止时的造型。 |
| `onStart` | object | 每个动画周期开始时触发一次动作。当前常用 `{"play": "soundName"}`。 |
| `onPlay` | object | 动画播放期间启动并随动画停止的循环声音。当前常用 `{"play": "soundName"}`。 |

动画可以绑定声音：

```json
{
  "fAnimations": {
    "walk": {
      "frameFrom": "walk-0",
      "frameTo": "walk-7",
      "frameFps": 12,
      "isLoop": true,
      "onStart": {"play": "step"},
      "onPlay": {"play": "grassWalk"}
    }
  }
}
```

- `onStart` 是一次性声音，每轮动画开始时触发。
- `onPlay` 是随动画生命周期播放的循环声音，动画停止或进入下一轮时会停止/重启。
- `play` 的值对应 `assets/sounds/<声音名>/index.json` 中的声音名。

### 9.6 角色碰撞和物理字段

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `physicsMode` | string | 角色物理模式；项目也可以在脚本中设置。 |
| `mass` | number | 质量。 |
| `friction` | number | 摩擦系数/缩放。 |
| `airDrag` | number | 空气阻力。 |
| `gravity` | number | 角色自身重力缩放。 |
| `collisionShapeType` | string | 实体碰撞形状：`none`、`auto`、`circle`、`rect`、`capsule` 或 `polygon`。 |
| `collisionShapeParams` | number[] | 形状参数，格式见下表。 |
| `collisionPivot` | object | 碰撞形状相对角色逻辑原点的偏移。 |
| `collisionLayer` | integer | 角色所在的碰撞层位掩码。 |
| `collisionMask` | integer | 角色希望检测的碰撞层位掩码。 |
| `triggerShapeType` | string | 触发器形状，取值与 `collisionShapeType` 相同。 |
| `triggerShapeParams` | number[] | 触发器形状参数。 |
| `triggerPivot` | object | 触发器相对角色逻辑原点的偏移。 |
| `triggerLayer` | integer | 触发器所在层位掩码。 |
| `triggerMask` | integer | 触发器检测的层位掩码。 |

形状参数格式：

| 形状 | 参数 |
| --- | --- |
| `rect` | `[宽度, 高度]` |
| `circle` | `[半径]` |
| `capsule` | `[半径, 高度]` |
| `polygon` | `[x0, y0, x1, y1, ...]` |
| `auto` | 根据造型自动生成，不需要手工参数 |
| `none` | 不创建对应形状 |

## 10. 声音级 `assets/sounds/<声音名>/index.json`

```json
{
  "path": "jump.wav",
  "rate": 44100,
  "sampleCount": 22050
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `path` | string | 声音文件路径，相对于当前声音目录。 |
| `rate` | integer | 采样率元数据。 |
| `sampleCount` | integer | 采样数量元数据。 |

脚本通过目录名引用声音：

```coffee
play "jump"
playAndWait "hit"
```

声音配置和音频文件采用按需加载：第一次播放某个名字时，运行时读取 `sounds/<名字>/index.json`。

## 11. `index_pack.json` 的作用和优先级

`assets/index_pack.json` 将项目配置、角色配置、声音配置和字体配置聚合到一个 JSON 中。典型结构如下：

```json
{
  "backdropIndex": 0,
  "backdrops": [],
  "zorder": ["Player"],
  "sprites": {
    "Player": {
      "costumes": [],
      "costumeIndex": 0,
      "x": 0,
      "y": 0
    }
  },
  "sounds": {
    "jump": {
      "path": "jump.wav",
      "rate": 44100,
      "sampleCount": 22050
    }
  },
  "fonts": {
    "Game Font": {
      "faces": [{"path": "game-font.ttf"}]
    }
  }
}
```

运行时规则：

1. 不存在 `index_pack.json` 时，读取 `assets/index.json` 以及各子目录中的 `index.json`。
2. 存在 `index_pack.json` 时，项目、`sprites`、`sounds`、`fonts` 中已内嵌的配置优先。
3. packed 配置缺少的子项可以回退到对应的源 `index.json`。
4. 项目级字段合并时，`index_pack.json` 中的同名字段覆盖 `assets/index.json`。
5. 图片、音频等二进制文件仍从原资源目录读取。

因此，开发时不要只修改 `assets/index.json`，却保留一份过期的 `assets/index_pack.json`。应使用项目工具重新生成聚合配置，或者确保当前运行方式只使用源配置。

## 12. `main.spx` 与角色脚本的职责

`main.spx` 适合放置：

- 所有角色共享的变量。
- 游戏分数、关卡状态等舞台级状态。
- 游戏启动事件。
- 需要从 `zorder` 的 `sprites` 条目绑定的角色数组。

```coffee
var (
	score   int
	Player  Player
	Enemies []Enemy
)

onStart => {
	score = 0
}
```

角色脚本适合放置角色自己的状态和事件：

```coffee
var (
	health int
)

onStart => {
	health = 100
}

onMsg "damage", => {
	health--
}
```

## 13. 常见错误检查清单

### 13.1 角色不显示

- 检查 `<角色名>.spx` 是否存在。
- 检查 `assets/sprites/<角色名>/index.json` 是否存在。
- 检查脚本名、资源目录名、`zorder` 名称的大小写是否完全一致。
- 检查角色 `visible` 是否为 `true`。
- 检查 `costumeIndex` 是否越界。
- 检查造型 `path` 是否相对于角色目录书写。
- 检查角色坐标是否位于世界范围内。

### 13.2 背景不显示或尺寸异常

- 检查背景 `path` 是否相对于 `assets/`。
- 检查 `backdropIndex` 是否从 `0` 开始且没有越界。
- 检查 `map.width` 和 `map.height`。
- 检查 `map.mode` 是否为支持的值。
- 检查 `bitmapResolution` 是否与资源导出倍率一致。
- 检查是否存在覆盖源配置的旧 `index_pack.json`。

### 13.3 修改 `index.json` 后没有生效

- 优先检查 `assets/index_pack.json` 是否存在。
- 不要修改 `project/go/main.go`，它会被重新生成。
- 清理或重新生成构建产物后再运行。
- 确认当前运行命令的 `--path` 指向正确项目目录，而不是另一个 `.tmp` 副本。

### 13.4 JSON 能解析但字段没有效果

- JSON 字段名区分大小写，例如 `backdropIndex` 不能写成 `backdropindex`。
- 当前解析器会忽略未识别字段，所以拼写错误可能不会直接报错。
- 数字、布尔值和字符串类型要正确，例如 `visible` 应写 `true`，不能写 `"true"`。
- JSON 不允许尾随逗号，也不允许 `//` 注释。

## 14. 推荐实践

1. 把 `assets/index.json` 当作项目级源配置。
2. 每个角色只维护自己的 `assets/sprites/<名称>/index.json`。
3. 脚本名、角色目录名和 `zorder` 名称保持完全一致。
4. 所有资源路径尽量使用相对 POSIX 路径。
5. 明确配置 `map.width`、`map.height`、`run.width` 和 `run.height`。
6. 普通图片逐帧角色使用 `costumes`；大量规则帧使用 `costumeSet`。
7. 把 `project/`、`.temp/` 和自动生成 Go 文件视为可再生内容。
8. 由打包工具维护 `index_pack.json`，避免手工双写造成配置不一致。
9. 提交代码前至少验证一次原生运行和目标导出平台，尤其要检查大小写和资源路径。

## 15. 与 `01-Weather` 项目的对应关系

`tutorial/01-Weather` 的核心结构是：

```text
tutorial/01-Weather/
├── main.spx
├── Jaime.spx
├── Kai.spx
└── assets/
    ├── index.json
    ├── index_pack.json
    ├── 6.png
    └── sprites/
        ├── Jaime/
        │   ├── index.json
        │   ├── 1.png
        │   └── 2.png
        └── Kai/
            ├── index.json
            ├── 3.png
            └── 4.png
```

- `assets/index.json` 中的 `backdrops[0].path` 是 `6.png`，所以背景文件是 `assets/6.png`。
- `backdropIndex: 0` 表示默认使用第一张背景。
- `zorder` 中依次注册 `Jaime` 和 `Kai`。
- `Jaime.spx` 对应 `assets/sprites/Jaime/index.json`。
- `Kai.spx` 对应 `assets/sprites/Kai/index.json`。
- 根目录的 `1.jpg`、`2.jpg` 没有被资源配置引用，它们是教程截图，不是游戏运行资源。
