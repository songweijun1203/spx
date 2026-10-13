# Web 模式 Godot 到 Go 回调分类说明

本文只讨论 SPX Web 模式，整理 Godot/C++ 经 JavaScript 到 Go WASM 的回调协议，
并说明 `gdspxDispatch`、批量接触事件入口和 `engine.CallbackInfo` 之间的关系。

## 结论

- `gdspxDispatch` 生成了 `CallbackInfo` 全部 43 种事件名的分支。从“它能接受哪些
  `CallbackInfo` 事件”看，覆盖是完整的。
- 从实际 Web 传输路径看，`gdspxDispatch` 不是唯一入口：36 种回调走常规单事件
  分发，6 种碰撞/触发回调优先走批量入口，`OnEngineStart` 使用特殊启动路径。
- `CallbackInfo` 覆盖当前 SPX Web 绑定协议声明的全部 43 种业务回调；它和 Web
  生成分发器的事件名集合一致。
- 这里的“全部”仅指 **SPX 明确暴露给 Go 的回调协议**，不包括 Godot 的全部
  `Signal`、`Notification`、输入事件或任意扩展信号。
- 字段存在只代表协议能够承载该事件，不代表当前 Godot 侧一定会发出，也不代表
  Go 公共层一定已经处理。下表单独标记当前落地状态。

## Web 分发路径总览

| 类型 | 数量 | 主要入口 | 说明 |
| --- | ---: | --- | --- |
| 常规单事件 | 36 | `gdspxDispatch` | JavaScript 传入事件名和独立参数 |
| 碰撞与触发 | 6 | `gdspxContactEvents` | 通常逐帧批量传输；批量入口缺失时回退到 `gdspxDispatch` |
| 引擎启动 | 1 | 特殊启动路径 | 分发器支持 `OnEngineStart`，解释器模式会人工调用；Godot Web 回调还负责启动 Go WASM |
| Web 宿主通知 | 不属于 43 项 | `notifyRuntime` | reset 完成、panic、exit 等通知不进入 `CallbackInfo` |

### 常规单事件

```text
Godot/SPX C++
  -> godot_js_spx_on_xxx
  -> GodotGdspx.dispatch("OnXxx", ...)
  -> FFI.gdspx_dispatch
  -> gdspxDispatch
  -> binding/web.callbacks.OnXxx
  -> internal/gdengine 的 onXxx
```

`gdspxDispatch` 按第一个参数中的事件名选择 `CallbackInfo` 字段，并完成
`GdObj`、`GdInt`、`GdFloat`、`GdBool` 和 `GdString` 到 Go 类型的转换。未知事件名
会被忽略；已知事件仍要求调用方传入正确数量和类型的参数。

### 例外路径

以下通道不应概括为“全部经过 `gdspxDispatch`”：

1. 碰撞和触发事件优先进入 JavaScript 接触事件队列，在每帧更新前批量调用
   `gdspx_on_contact_events -> gdspxContactEvents`，再直接选择
   `callbacks.OnCollisionXxx` 或 `callbacks.OnTriggerXxx`。批量入口不可用时，
   JavaScript 才逐条回退到 `gdspxDispatch`。
2. `OnEngineStart` 是特殊启动链路。`gdspxDispatch` 有对应分支；Godot Web 回调还要
   先负责加载 Go WASM，解释器模式则由 `LinkSession.Run` 人工构造该事件并调用
   `gdspxDispatch`。
3. `gdspx_on_runtime_reset`、`gdspx_on_runtime_panic` 和 `gdspx_on_runtime_exit`
   是 Web 宿主通知，通过 `notifyRuntime` 发送，不属于 `CallbackInfo`。
4. `go_wasm_init` 是 Go WASM 初始化握手，`gdspx_on_contact_events` 是批量传输入口，
   二者都不是 `CallbackInfo` 中的业务回调。

## Web 参数约定

| Go 类型 | JavaScript 表示与转换 | 含义 |
| --- | --- | --- |
| `int64` 对象参数 | `{low, high}`，由 `JsToGdObj` / `JsToGdInt` 还原 | 对象 ID、按键 ID 或鼠标按钮 ID |
| `float64` | JavaScript `Number`，由 `JsToGdFloat` 转换 | 帧间隔、物理帧间隔或轴值 |
| `bool` | JavaScript 布尔值或整数，由 `JsToGdBool` 转换 | 暂停、开关等二值状态 |
| `string` | JavaScript `String`，由 `JsToGdString` 转换 | 类型名、InputMap action 名或文本 |

“当前状态”列使用以下含义：

- **生效**：Godot/SPX 当前会发出，Go 公共层也有实际处理。
- **部分生效**：链路会触发，但当前处理仅覆盖部分生命周期或特殊平台路径。
- **预留**：协议、Native/Web 入口已经存在，但当前缺少 Godot 发出点或 Go 处理器。

## 1. 引擎生命周期

| 回调 | Go 签名 | 触发语义 | 当前状态 |
| --- | --- | --- | --- |
| `OnEngineStart` | `func()` | C++ Manager 启动后，通知 Go 开始本局游戏 | **生效**；Web 启动使用前述特殊链路 |
| `OnEngineUpdate` | `func(delta float64)` | Godot 每个逻辑/渲染帧；C++ Manager 更新完成后触发 | **生效**；Web 先刷新接触事件和输入快照 |
| `OnEngineFixedUpdate` | `func(delta float64)` | Godot 每个物理帧；把固定步长交给 Go Manager 和精灵代理 | **生效**；根引擎当前未额外注册 core fixed-update 处理器 |
| `OnEngineDestroy` | `func()` | C++ 资源清理前，通知 Go 结束游戏、精灵和 Manager | **生效** |
| `OnEngineDestroyed` | `func()` | C++ `SpxEngine` 清理完成后通知 Go | **生效** |
| `OnEngineReset` | `func()` | 单局 reset 清理开始时触发，Godot 进程可继续复用 | **生效** |
| `OnEnginePause` | `func(isPaused bool)` | SPX/Godot 暂停状态发生变化 | **生效** |

## 2. 场景与精灵生命周期

| 回调 | Go 签名 | 触发语义 | 当前状态 |
| --- | --- | --- | --- |
| `OnSceneSpriteInstantiated` | `func(id int64, typeName string)` | Godot 从场景实例化精灵后，把对象 ID 和类型名绑定到 Go 代理 | **生效** |
| `OnSpriteReady` | `func(id int64)` | 精灵节点准备完成，Go 查找代理并调用 `OnStart` | **生效** |
| `OnSpriteUpdated` | `func(delta float64)` | 预留的精灵逻辑帧通知 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |
| `OnSpriteFixedUpdated` | `func(delta float64)` | 预留的精灵物理帧通知 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |
| `OnSpriteDestroyed` | `func(id int64)` | 精灵销毁后从 Go 代理注册表删除 | **生效** |

## 3. 动画、特效与屏幕可见性

| 回调 | Go 签名 | 触发语义 | 当前状态 |
| --- | --- | --- | --- |
| `OnSpriteFramesSetChanged` | `func(id int64)` | `SpriteFrames` 资源集合变化 | **生效**；调用精灵的虚方法和公开处理器 |
| `OnSpriteAnimationChanged` | `func(id int64)` | 当前动画名称变化 | **生效** |
| `OnSpriteFrameChanged` | `func(id int64)` | 当前动画帧变化 | **生效**；C++ 同时刷新逐帧轴心和 Shader UV |
| `OnSpriteAnimationLooped` | `func(id int64)` | 动画完成一次循环 | **生效** |
| `OnSpriteAnimationFinished` | `func(id int64)` | 非循环动画播放完成 | **生效** |
| `OnSpriteVfxFinished` | `func(id int64)` | 视觉特效播放完成 | **预留**；Go 处理器已存在，但 C++ `_on_sprite_vfx_finished` 当前未发出回调 |
| `OnSpriteScreenEntered` | `func(id int64)` | 精灵进入屏幕可见区域 | **生效** |
| `OnSpriteScreenExited` | `func(id int64)` | 精灵离开屏幕可见区域 | **生效** |

## 4. 原始输入与 InputMap action

| 回调 | Go 签名 | 触发语义 | 当前状态 |
| --- | --- | --- | --- |
| `OnMousePressed` | `func(buttonID int64)` | Godot 收到鼠标按钮按下 | **生效**；继续交给 core 输入处理器 |
| `OnMouseReleased` | `func(buttonID int64)` | Godot 收到鼠标按钮释放 | **生效** |
| `OnKeyPressed` | `func(keyID int64)` | Godot 收到键盘按下 | **生效**；Web 同时更新键盘快照 |
| `OnKeyReleased` | `func(keyID int64)` | Godot 收到键盘释放 | **生效**；Web 同时更新键盘快照 |
| `OnActionPressed` | `func(actionName string)` | InputMap action 处于按下状态 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |
| `OnActionJustPressed` | `func(actionName string)` | InputMap action 本帧刚按下 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |
| `OnActionJustReleased` | `func(actionName string)` | InputMap action 本帧刚释放 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |
| `OnAxisChanged` | `func(actionName string, value float64)` | InputMap 轴值变化 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |

## 5. 碰撞与触发器

`selfID` 是接收事件的精灵，`otherID` 是另一精灵。Web 对这六种事件采用批量队列，
因此它们即使最终进入同一个 `CallbackInfo`，通常也不经过 `gdspxDispatch`。

| 回调 | Go 签名 | 触发语义 | 当前状态 |
| --- | --- | --- | --- |
| `OnCollisionEnter` | `func(selfID, otherID int64)` | 实体碰撞开始 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |
| `OnCollisionStay` | `func(selfID, otherID int64)` | 实体碰撞持续 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |
| `OnCollisionExit` | `func(selfID, otherID int64)` | 实体碰撞结束 | **预留**；C++ 当前没有发出点，Go 处理器仅记录日志 |
| `OnTriggerEnter` | `func(selfID, otherID int64)` | `Area2D` 或像素碰撞候选确认进入 | **生效**；分发到精灵的 `V_OnTriggerEnter` 和 `OnTriggerEnter` |
| `OnTriggerStay` | `func(selfID, otherID int64)` | 触发区域持续接触 | **预留**；C++ 当前没有发出点，Go 处理器为空 |
| `OnTriggerExit` | `func(selfID, otherID int64)` | `Area2D` 或像素碰撞对退出 | **生效**；分发到精灵的 `V_OnTriggerExit` 和 `OnTriggerExit` |

## 6. UI 生命周期与交互

| 回调 | Go 签名 | 触发语义 | 当前状态 |
| --- | --- | --- | --- |
| `OnUiReady` | `func(id int64)` | UI 包装器创建并加入 Godot 管理表 | **部分生效**；C++ 会发出，但 `bindCallbacks` 当前没有绑定 Go 处理器 |
| `OnUiUpdated` | `func(id int64)` | 预留的 UI 更新通知 | **预留**；C++ 没有发出点，`bindCallbacks` 也未绑定 |
| `OnUiDestroyed` | `func(id int64)` | UI 包装器销毁或 reset 清理 | **生效**；从 Go UI 注册表删除 |
| `OnUiPressed` | `func(id int64)` | UI 控件按下 | **预留**；Go 处理器已存在，C++ 当前没有发出点 |
| `OnUiReleased` | `func(id int64)` | UI 控件释放 | **预留**；Go 处理器已存在，C++ 当前没有发出点 |
| `OnUiHovered` | `func(id int64)` | 指针悬停 UI 控件 | **预留**；Go 处理器已存在，C++ 当前没有发出点 |
| `OnUiClicked` | `func(id int64)` | `Button::pressed` 信号经绑定所有权检查后触发 | **生效**；分发到 `V_OnUiClick` 和 `OnUiClick` |
| `OnUiToggle` | `func(id int64, isOn bool)` | Toggle 状态变化 | **预留**；Go 处理器已存在，C++ 当前没有发出点 |
| `OnUiTextChanged` | `func(id int64, text string)` | 输入文本变化 | **预留**；Go 处理器已存在，C++ 当前没有发出点 |

## Web 定义与生成关系

回调协议涉及以下位置：

| 层 | 文件 | 作用 |
| --- | --- | --- |
| ABI 回调定义 | `internal/cmd/codegen/generate/gdext/gdextension_spx_ext.h.tmpl` | 定义 43 种 SPX 回调，是 Web 代码生成输入 |
| Go 协议 | `pkg/spx/pkg/engine/interface.go` | 定义 `CoreCallbackInfo` 和 `CallbackInfo` |
| Go 公共绑定 | `internal/gdengine/callbacks.go` | 用 `bindCallbacks` 连接协议字段与业务处理器 |
| Web 生成模板 | `internal/cmd/codegen/generate/webffi/callbacks.go.tmpl` | 从 ABI 回调定义生成事件名和分发分支 |
| Web 分发器 | `internal/gdengine/binding/web/callbacks.gen.go` | `gdspxDispatch` 的生成结果 |
| Web JS 桥 | `godot_modules/spx/web/js/libs/library_godot_gdspx.js` | 类型转换、普通分发、接触事件批处理和宿主通知 |
| Web 批量入口 | `internal/gdengine/binding/web/contact_events.go` | 解码碰撞/触发事件批次并直接调用对应 `CallbackInfo` 字段 |

新增或删除回调时，至少要同时确认四件事：ABI 模板是否声明、Godot 是否真正发出、
`CallbackInfo`/`bindCallbacks` 是否接收、生成后的 Web 分发器与批量入口是否一致。仅增加
一个结构体字段并不能形成可用的端到端回调。
