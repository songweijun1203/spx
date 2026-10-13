/**************************************************************************/
/*  spx.h                                                                 */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/
/* Copyright (c) 2014-present Godot Engine contributors (see AUTHORS.md). */
/* Copyright (c) 2007-2014 Juan Linietsky, Ariel Manzur.                  */
/*                                                                        */
/* Permission is hereby granted, free of charge, to any person obtaining  */
/* a copy of this software and associated documentation files (the        */
/* "Software"), to deal in the Software without restriction, including    */
/* without limitation the rights to use, copy, modify, merge, publish,    */
/* distribute, sublicense, and/or sell copies of the Software, and to     */
/* permit persons to whom the Software is furnished to do so, subject to  */
/* the following conditions:                                              */
/*                                                                        */
/* The above copyright notice and this permission notice shall be         */
/* included in all copies or substantial portions of the Software.        */
/*                                                                        */
/* THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,        */
/* EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF     */
/* MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. */
/* IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY   */
/* CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,   */
/* TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE      */
/* SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.                 */
/**************************************************************************/

#ifndef SPX_H
#define SPX_H
#include "core/templates/safe_refcount.h"
#include "spx_pending_controls.h"

class MainLoop;

// SPX 模块的进程级生命周期门面。
// 它把 Godot 主循环阶段转发给 SpxEngine，并把任意线程提交的重置、暂停等控制命令
// 汇聚到 Godot 主线程执行；本类不拥有场景节点，具体资源由 SpxEngine 和各 Manager 管理。
// 顶层调用链：Godot main/Go gdengine API -> Spx -> SpxEngine -> 各 SpxManager。
class Spx {
	friend class SpxEngine;

	// GDExtension 接口表是否已经注入。接口表是进程级资源，Godot 没有对应的移除 API。
	static inline bool extension_functions_registered = false;
	// SPX 是否已挂接到有效 SceneTree；SafeFlag 允许非主线程只读查询。
	static inline SafeFlag initialized{ false };
	// 调试绘制总开关；由平台/Go 调试 API 设置，在主线程由相关节点读取。
	static inline bool debug_mode = false;
	// 跨线程控制命令邮箱；生产者可来自 Go runtime，只有 Godot 主循环消费命令。
	static inline SpxPendingControls pending_controls;

public:
	// 直接调用方：initialize_spx_module(CORE)；顶层调用方：Godot main 初始化流程。
	static void register_extension_functions();
	// 直接调用方：uninitialize_spx_module(CORE)；顶层调用方：Godot main 退出流程。
	static void unregister_extension_functions();
	// 直接调用方：initialize_spx_module(SCENE)；顶层调用方：Godot main 初始化流程。
	static void register_main_loop_callbacks();
	// 直接调用方：uninitialize_spx_module(SCENE)；顶层调用方：Godot main 退出流程。
	static void unregister_main_loop_callbacks();
	static bool is_initialized() { return initialized.is_set(); }
	static bool is_debug_mode() { return debug_mode; }
	static void set_debug_mode(bool enable);

	// 注册 SPX 自定义 Node/资源辅助类型；必须在 Godot SCENE 初始化级别调用。
	static void register_types();
	// 以下四个入口由 MainLoopPhaseCallbackBus 直接调用，顶层调用方是 Godot 主循环。
	static void on_start(MainLoop *p_main_loop);
	static void on_fixed_update(double delta);
	static void on_update(double delta);
	static void on_destroy();

	// 以下控制入口的直接调用方是 SpxExtMgr/Web 会话层；顶层调用方是 Go 游戏运行时。
	// 非主线程调用只投递 pending_controls，实际 SceneTree 操作延后到 on_update()。
	static void reset(int exit_code);
	static void restart();

	static void pause();
	static void resume();
	static bool is_paused();
	static void next_frame();
};

#endif // SPX_H
