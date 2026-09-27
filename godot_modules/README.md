# Godot custom modules

SPX-owned Godot modules live here instead of in the upstream Godot checkout.
The default engine build loads the `spx` module itself with Godot's
`custom_modules` option, so sibling modules are not selected accidentally.
`SPX_MODULE_SRC` may point at another `spx` module source directory; relative
overrides are resolved from the SPX repository root.

`spx/spx_scons_profile.json` is the shared source of truth for static SCons
arguments used by local engine builds, legacy Docker builds, and Godot release
workflows. The versioned profile contains ordered `key=value` arrays for common,
editor, and template release settings. Keep target, platform, architecture, Web
threading, and the `custom_modules` path in the caller: the module path must
remain one argument even when it contains spaces. The common profile disables
recursive custom-module discovery because `custom_modules` points directly at
`spx`.

The `spx` module, its private third-party dependencies, Web bridge, recorder,
and module tests are stored under `godot_modules/spx`. Keep generated bindings
in that module and run `make generate` after changing manager declarations.

## SPX 模块中文导读

`godot_modules/spx` 是 Go SPX 运行时与 Godot 引擎之间的适配层。它不重新实现
Godot 的渲染、输入、音频或物理系统，而是把 Go 侧面向 Scratch/SPX 的对象模型和
同步命令，转换成 Godot 能直接执行的节点、资源、服务器调用与主循环回调。

主调用链如下：

```text
Go 游戏 API
  -> Go runtime/同步缓冲/engine Manager
  -> 原生 gdextension_spx_ext.* 或 Web godot_js_spx.* ABI
  -> SpxEngine
  -> Spx*Mgr
  -> Godot SceneTree / Resource / Server
```

事件按相反方向返回：Godot 的生命周期、输入、碰撞、动画和 UI 信号先进入
`SpxCallbackProxy`，再通过原生函数指针或 Emscripten JS Library 通知 Go runtime。

### 功能与 Go 侧需求

- `SpxEngine` 和 `SpxManager`：统一创建各 Manager，并把 Go runtime 的
  awake/update/fixed-update/reset/destroy 映射到 Godot 主循环阶段。
- `SpxSpriteMgr`、`SpxSprite`：响应 Go 侧角色创建、造型、位置、旋转、缩放、层级、
  动画、特效和物理同步，落到 Godot `Node2D`、`AnimatedSprite2D`、材质及物理节点。
- `SpxSceneMgr`、`SpxLayerSorter`：响应场景切换、轻量渲染/静态精灵、前后层排序、
  场景边界计算和离屏 PNG 导出。
- `SpxInputMgr`、`SpxInputProxy`：把 Godot Viewport/InputMap 事件转换为 Go 侧按键、
  鼠标、Action 与 Axis 回调，并提供主动查询。
- `SpxAudioMgr`、`SpxAudioBusPool`：把 Go 侧播放、暂停、音量、声像和生命周期请求
  转为 `AudioStreamPlayer` 与 `AudioServer` 总线操作。
- `SpxPhysicsMgr`、碰撞代理：承接 Go 侧碰撞体配置、空间查询和碰撞/触发回调，复用
  Godot PhysicsServer、DirectSpaceState 与信号机制。
- `SpxPenMgr`、`SpxPenSurface`：实现 Go 侧落笔、移动、颜色/粗细、印章、擦除和截图，
  通过 Canvas RID 批量绘制以降低跨语言逐点调用开销。
- `SpxTilemapMgr`、`SpxTilemapparserMgr`：实现 Go 侧运行时铺砖、擦除、分层、碰撞、
  撤销/重做，以及 JSON 地图到 Godot `TileSet`/`TileMapLayer` 的加载。
- `SpxResMgr`、SVG/字体封装：统一 Go 资源路径、纹理/字体缓存、SVG 栅格化和中文字体，
  屏蔽原生文件系统、PCK 与 Web 虚拟文件系统差异。
- `SpxUiMgr` 和 UI binding：把 Go 侧控件创建、属性同步及点击/文本等事件映射到
  Godot `Control` 节点与信号。
- `recorder/`：为 Go 录制 API 和 Godot `--write-movie` 提供实时音视频采集、独立线程
  写入、Web `MediaRecorder` 适配及停止后的音视频合并。
- `web/`：在 Go WASM 与 Godot WASM 之间管理线性内存、字符串/数组 wrapper、回调表、
  浏览器文件系统、Canvas、输入和录制能力。

### 为什么需要这些封装

Go 侧关心的是稳定的 SPX 语义和跨平台 API，不应持有 Godot `Object` 指针，也不应
依赖节点何时进入场景树、资源如何引用计数、输入怎样传播或物理查询在哪个线程执行。
本模块集中处理以下差异：

- 用整数对象 ID 和定长 ABI 类型跨越 Go/C++/WASM 边界，避免暴露 C++ 对象布局。
- 把大量属性更新合并后在 Godot 主线程应用，减少 FFI 次数并满足 SceneTree 线程约束。
- 把 Godot 信号和引擎通知转换为 Go 可注册的回调，维持双向生命周期一致性。
- 用 `Ref`、`memnew`、`queue_free` 和 RID 的对应规则明确资源与节点所有权。
- 在 Web 端补齐 WASM 线性内存、浏览器安全沙盒和异步加载带来的平台差异。

### 阅读与修改约定

源码注释中的“直接调用方”表示紧邻当前函数或类的下一层调用者，“顶层调用方”表示
触发整条链路的 Go API、Godot 主循环/信号或浏览器事件。`GDCLASS`、`_bind_methods`、
`_notification`、`RefCounted`、`queue_free`、`call_deferred` 等旁注说明的是 Godot
规定的使用方式，而非 SPX 自定义约定。

`gdextension_spx_ext.*`、`spx_callback_defaults.gen.h`、`web/godot_js_spx.cpp` 和
`web/js/engine/gdspx.js` 属于生成绑定。修改 Manager 中的 `SPX_BIND` 声明后应运行
`make generate`，不要把手写业务逻辑放入这些生成文件。`thirdparty/` 保持上游源码，
模块级中文注释不应侵入第三方实现。
