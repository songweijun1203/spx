/**************************************************************************/
/*  spx.cpp                                                               */
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

#include "spx.h"

#include "core/io/dir_access.h"
#include "core/object/class_db.h"
#include "core/os/thread.h"
#include "main/main_loop_phase_callback_bus.h"
#include "scene/main/node.h"
#include "scene/main/scene_tree.h"
#include "scene/main/window.h"

#include "gdextension_spx_ext.h"
#include "spx_callback_proxy.h"
#include "spx_collision_debug_overlay.h"
#include "spx_draw_tiles.h"
#include "spx_engine.h"
#include "spx_input_proxy.h"
#include "spx_path_finder.h"
#include "spx_sprite.h"
#include "spx_ui.h"
#include "spx_list_monitor.h"

// SPX 在 SceneTree 中的锚点节点。
// SpxEngine 创建的 Manager 根节点都挂在其下；节点所有权交给 SceneTree，退出时由 Godot
// 随树释放。Godot 规定 Node 只有加入 SceneTree 后才能可靠访问树、Viewport 和场景生命周期。
class SpxEngineNode : public Node {
	GDCLASS(SpxEngineNode, Node);
};

#define SPX_ENGINE SpxEngine::get_singleton()

namespace {
// 主循环阶段总线的进程级注册句柄；只记录注册关系，不拥有总线或回调对象。
MainLoopPhaseCallbackBus::RegistrationID main_loop_callback_registration = MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID;

// 直接调用方：MainLoopPhaseCallbackBus；顶层调用方：Godot 主循环 start 阶段。
void _spx_main_loop_start(void *, MainLoop *p_main_loop) {
	Spx::on_start(p_main_loop);
}

// 直接调用方：MainLoopPhaseCallbackBus；顶层调用方：Godot 物理帧循环。
void _spx_main_loop_fixed_update(void *, double p_delta) {
	Spx::on_fixed_update(p_delta);
}

// 直接调用方：MainLoopPhaseCallbackBus；顶层调用方：Godot 每帧逻辑循环。
void _spx_main_loop_update(void *, double p_delta) {
	Spx::on_update(p_delta);
}

// 直接调用方：MainLoopPhaseCallbackBus；顶层调用方：Godot 主循环销毁流程。
void _spx_main_loop_destroy(void *) {
	Spx::on_destroy();
}

inline bool _is_spx_engine_ready() {
	return Spx::is_initialized() && SpxEngine::is_initialized();
}
} // namespace

// 最近调用方：initialize_spx_module(CORE)；最顶层入口：Godot C++ main 的模块初始化。
void Spx::register_extension_functions() {
	if (extension_functions_registered) {
		return;
	}

	// Module CORE initialization runs immediately before Godot builds the base
	// GDExtension interface table. Register the SPX-owned entries here so core
	// no longer needs an SPX callback or alias functions.
	// CORE 初始化紧邻 Godot 构造基础 GDExtension 接口表；在这里插入 SPX 自有接口，
	// Godot core 就不必再依赖 SPX 专用回调或别名函数。
	gdextension_spx_setup_interface();
	extension_functions_registered = true;
}

void Spx::unregister_extension_functions() {
	// Godot's GDExtension interface registry is process-scoped and has no
	// matching removal API. Keep this guard set so a module reinitialization
	// cannot try to insert the same SPX function names a second time.
	// Godot 规则：GDExtension 接口注册表是进程级的且没有移除 API；这里故意不清除标志，
	// 防止测试或模块重载时重复插入同名接口。
}

// 把 SPX 的 start/fixed_update/update/destroy 接到 Godot 主循环阶段总线。
// 最近调用方：initialize_spx_module(SCENE)。
// 最顶层入口：engine.js -> Module.callMain() -> Godot SCENE 模块初始化。
void Spx::register_main_loop_callbacks() {
	if (main_loop_callback_registration != MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID) {
		return;
	}

	MainLoopPhaseCallbackBus::Callbacks callbacks;
	callbacks.start = &_spx_main_loop_start;
	callbacks.fixed_update = &_spx_main_loop_fixed_update;
	callbacks.update = &_spx_main_loop_update;
	callbacks.destroy = &_spx_main_loop_destroy;
	main_loop_callback_registration = get_main_loop_phase_callback_bus().register_callbacks(callbacks);
}

void Spx::unregister_main_loop_callbacks() {
	if (main_loop_callback_registration == MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID) {
		return;
	}

	get_main_loop_phase_callback_bus().unregister_callbacks(main_loop_callback_registration);
	main_loop_callback_registration = MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID;
}

// 直接调用方：SpxDebugMgr 的调试 API；顶层调用方：Go 侧调试配置。
// 同步通知碰撞调试覆盖层，使既有节点立即反映新模式。
void Spx::set_debug_mode(bool enable) {
	debug_mode = enable;
	spx_collision_debug_mode_changed(enable);
}

// 直接调用方：initialize_spx_module(SCENE)；顶层调用方：Godot 模块初始化框架。
// Godot 规定：可实例化/可反射的 Object 派生类须先向 ClassDB 注册；internal 类型不暴露给用户。
void Spx::register_types() {
	ClassDB::register_class<SpxListMonitor>();
	ClassDB::register_class<SpxSprite>();
	ClassDB::register_internal_class<SpxCollisionDebugOverlay>();
	ClassDB::register_class<SpxInputProxy>();
	ClassDB::register_class<SpxDrawTiles>();
	ClassDB::register_class<SpxPathFinder>();
	ClassDB::register_class<PathDebugDrawer>();
	ClassDB::register_class<SpxCallbackProxy>();
}

// SPX 底层生命周期的真正启动点。
// 最近调用方：_spx_main_loop_start()，它由 Godot 主循环阶段总线调用。
// 最顶层入口：Godot SceneTree 开始运行；Web 侧起点是 Engine.start() 的 Module.callMain()。
// 本函数创建 SpxEngineNode 并唤醒 C++ Managers，最后才发出 on_engine_start 回调。
void Spx::on_start(MainLoop *p_main_loop) {
	if (initialized.is_set() || !SpxEngine::is_initialized()) {
		return;
	}

	SceneTree *tree = Object::cast_to<SceneTree>(p_main_loop);
	if (tree == nullptr) {
		return;
	}
	Window *root = tree->get_root();
	if (root == nullptr) {
		return;
	}

	pending_controls.set_accepting(false);
	SpxEngineNode *new_node = memnew(SpxEngineNode);
	new_node->set_name("SpxEngineNode");
	root->add_child(new_node);
	SPX_ENGINE->set_root_node(tree, new_node);
	initialized.set();
	pending_controls.set_accepting(true);
	SPX_ENGINE->on_awake();
}

// 最近调用方：_spx_main_loop_fixed_update()；最顶层来源：Godot 每个物理帧。
void Spx::on_fixed_update(double delta) {
	if (!_is_spx_engine_ready()) {
		return;
	}

	SPX_ENGINE->on_fixed_update(delta);
}

// 最近调用方：_spx_main_loop_update()；最顶层来源：Godot 每个渲染/逻辑帧。
// 待处理的 restart/reset/pause 等控制命令会在正常 Update 之前消费。
void Spx::on_update(double delta) {
	if (!_is_spx_engine_ready()) {
		return;
	}

	// Consume each phase immediately before execution. Requests made by a
	// callback for a later phase still run in this update, as before.
	// 每个阶段都在执行前即时取走命令，因此前一阶段回调新提交的后续阶段命令仍可在本帧执行。
	if (pending_controls.take(SpxPendingControls::RESTART)) {
		SPX_ENGINE->restart();
		return;
	}
	int exit_code = 0;
	if (pending_controls.take(SpxPendingControls::RESET, &exit_code)) {
		SPX_ENGINE->on_reset(exit_code);
		return;
	}
	if (pending_controls.take(SpxPendingControls::PAUSE)) {
		SPX_ENGINE->pause();
	}
	if (pending_controls.take(SpxPendingControls::RESUME)) {
		SPX_ENGINE->resume();
	}
	if (pending_controls.take(SpxPendingControls::NEXT_FRAME)) {
		SPX_ENGINE->next_frame();
	}

	SPX_ENGINE->on_update(delta);
}

// 最近调用方：主循环 destroy 回调或模块反初始化兜底；最顶层来源：Godot main 退出。
void Spx::on_destroy() {
	// 销毁期间触发的 runtime 回调必须观察到 SPX 已不可用，否则回调可能在 Manager
	// 正在释放时重新进入普通 API。
	initialized.clear();
	pending_controls.set_accepting(false);

	if (SpxEngine::is_initialized()) {
		SpxEngine::shutdown();
	}
}

// 直接调用方：SpxExtMgr::request_reset() 或 Web 会话恢复逻辑；顶层调用方：Go runtime 退出/重置请求。
// Godot 规则：SceneTree 和 Node 只能在主线程变更，因此工作线程仅投递命令。
void Spx::reset(int exit_code) {
	if (!Thread::is_main_thread()) {
		pending_controls.submit(SpxPendingControls::RESET, exit_code);
		return;
	}
	if (_is_spx_engine_ready()) {
		SPX_ENGINE->on_reset(exit_code);
	}
}

// 直接调用方：SpxExtMgr::request_restart()；顶层调用方：Go/Web 新一局启动流程。
void Spx::restart() {
	if (!Thread::is_main_thread()) {
		pending_controls.submit(SpxPendingControls::RESTART);
		return;
	}
	if (_is_spx_engine_ready()) {
		SPX_ENGINE->restart();
	}
}

// 直接调用方：SpxExtMgr::pause()；顶层调用方：Go engine 控制 API。
void Spx::pause() {
	if (!Thread::is_main_thread()) {
		pending_controls.submit(SpxPendingControls::PAUSE);
		return;
	}
	if (_is_spx_engine_ready()) {
		SPX_ENGINE->pause();
	}
}

// 直接调用方：SpxExtMgr::resume()；顶层调用方：Go engine 控制 API。
void Spx::resume() {
	if (!Thread::is_main_thread()) {
		pending_controls.submit(SpxPendingControls::RESUME);
		return;
	}
	if (_is_spx_engine_ready()) {
		SPX_ENGINE->resume();
	}
}

// 直接调用方：SpxExtMgr::next_frame()；顶层调用方：Go 调试单步 API。
void Spx::next_frame() {
	if (!Thread::is_main_thread()) {
		pending_controls.submit(SpxPendingControls::NEXT_FRAME);
		return;
	}
	if (_is_spx_engine_ready()) {
		SPX_ENGINE->next_frame();
	}
}

bool Spx::is_paused() {
	// 直接调用方：SpxExtMgr::is_paused()；顶层调用方：Go engine 暂停状态查询。
	return pending_controls.is_paused();
}
