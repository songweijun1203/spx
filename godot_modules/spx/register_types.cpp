#include "register_types.h"

#include "recorder/register_recorder_types.h"
#include "spx.h"

#ifdef WEB_ENABLED
#include "web/spx_web_bridge.h"
#endif

// 初始化静态编译进 Godot Web WASM 的 SPX 模块。
// 最近调用方：Godot 的模块初始化框架会按 CORE -> SERVERS -> SCENE 等级调用。
// 最顶层入口：engine.js 的 Engine.start() -> Module.callMain() -> Godot C++ main。
// 注意：这里初始化的是 Godot/SPX 底层模块，不代表具体 .spx 游戏已经启动。
void initialize_spx_module(ModuleInitializationLevel p_level) {
	if (p_level == MODULE_INITIALIZATION_LEVEL_CORE) {
		// 注册 Go 主动调用 Godot 时使用的 spx_* 接口表。
		Spx::register_extension_functions();
		initialize_spx_recorder_core();
#ifdef WEB_ENABLED
		// Web 版在 CORE 阶段直接创建 SpxEngine 单例，并安装 C++ -> JS 回调表。
		spx_web_register_callbacks();
#endif
	} else if (p_level == MODULE_INITIALIZATION_LEVEL_SERVERS) {
		initialize_spx_recorder_servers();
	} else if (p_level == MODULE_INITIALIZATION_LEVEL_SCENE) {
		// 注册 SpxSprite 等 Godot 类型，并把 SPX 生命周期接入 Godot 主循环阶段总线。
		Spx::register_types();
		Spx::register_main_loop_callbacks();
	}
}

// 最近调用方：Godot 的模块反初始化框架；最顶层来源：Godot main 退出或初始化失败回滚。
void uninitialize_spx_module(ModuleInitializationLevel p_level) {
	if (p_level == MODULE_INITIALIZATION_LEVEL_SCENE) {
		Spx::unregister_main_loop_callbacks();
		// 正常情况由主循环 destroy 阶段负责清理；这里保留幂等兜底，以处理启动失败，
		// 以及测试中从未进入 SceneTree 的情况。
		Spx::on_destroy();
	} else if (p_level == MODULE_INITIALIZATION_LEVEL_SERVERS) {
		uninitialize_spx_recorder_servers();
	} else if (p_level == MODULE_INITIALIZATION_LEVEL_CORE) {
		uninitialize_spx_recorder_core();
		Spx::unregister_extension_functions();
	}
}
