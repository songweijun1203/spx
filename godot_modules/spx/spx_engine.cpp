/**************************************************************************/
/*  spx_engine.cpp                                                        */
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

#include "spx_engine.h"
#include "spx_callback_defaults.gen.h"

#include "core/os/memory.h"
#include "core/os/thread.h"
#include "scene/gui/texture_rect.h"
#include "scene/main/canvas_layer.h"
#include "scene/main/scene_tree.h"
#include "scene/main/window.h"
#include "servers/display_server.h"

#include "gdextension_spx_ext.h"
#include "spx.h"
#include "spx_audio_mgr.h"
#include "spx_callback_proxy.h"
#include "spx_camera_mgr.h"
#include "spx_debug_mgr.h"
#include "spx_input_mgr.h"
#include "spx_navigation_mgr.h"
#include "spx_pen_mgr.h"
#include "spx_physics_mgr.h"
#include "spx_platform_mgr.h"
#include "spx_res_mgr.h"
#include "spx_scene_mgr.h"
#include "spx_sprite_mgr.h"
#include "spx_tilemap_mgr.h"
#include "spx_tilemapparser_mgr.h"
#include "spx_ui_mgr.h"
#ifdef WEB_ENABLED
#include "web/spx_web_session.h"
#endif

// 直接调用方：Web spx_web_register_callbacks()（以及生命周期测试）；顶层调用方：Web 模块 CORE 初始化。
// 保存的是借用函数指针，panic 消息只在同步回调期间有效。
void SpxEngine::register_runtime_panic_callbacks(GDExtensionSpxGlobalRuntimePanicCallback callback) {
	_register_runtime_callback(&SpxEngine::on_runtime_panic, callback, __func__);
}

// 直接调用方：Web spx_web_register_callbacks()（以及生命周期测试）；顶层调用方：Web 模块 CORE 初始化。
void SpxEngine::register_runtime_exit_callbacks(GDExtensionSpxGlobalRuntimeExitCallback callback) {
	_register_runtime_callback(&SpxEngine::on_runtime_exit, callback, __func__);
}

// 直接调用方：Web spx_web_register_callbacks()（以及生命周期测试）；顶层调用方：Web 模块 CORE 初始化。
void SpxEngine::register_runtime_reset_callbacks(GDExtensionSpxGlobalRuntimeResetCallback callback) {
	_register_runtime_callback(&SpxEngine::on_runtime_reset, callback, __func__);
}

// 创建进程内唯一的 SpxEngine，并创建其持有的全部 C++ Manager。
// Web 直接调用方：spx_web_register_callbacks()；Native 直接调用方：生成的
// gdextension_spx_global_register_callbacks()。
// 顶层调用方：Web 为 engine.js -> Module.callMain() -> initialize_spx_module(CORE)；
// Native 为 Godot GDExtension 加载器 -> Go gdspx_init() -> registerEngineCallback()。
void SpxEngine::register_callbacks(GDExtensionSpxCallbackInfoPtr callback_ptr) {
	if (singleton != nullptr) {
		print_error("SpxEngine::register_callbacks failed, already initialized!");
		return;
	}
	singleton = memnew(SpxEngine);
	singleton->_initialize_managers();
	singleton->callbacks = (callback_ptr != nullptr) ? *(SpxCallbackInfo *)callback_ptr : get_default_spx_callbacks();
	singleton->global_id = 1;
	singleton->is_spx_paused = false;
	singleton->should_execute_single_frame = false;
}

// 销毁唯一引擎实例，并保证 Go 的 destroy/destroyed 回调包围 C++ 资源清理。
// 直接调用方：Spx::on_destroy()；顶层调用方：Godot 主循环 destroy 或模块反初始化。
// 必须在 Godot 主线程执行，因为 Manager 清理会操作 SceneTree 节点。
void SpxEngine::shutdown() {
	if (singleton == nullptr || singleton->shutting_down) {
		return;
	}

	SpxEngine *engine = singleton;
	engine->shutting_down = true;
	const auto on_engine_destroyed = engine->callbacks.func_on_engine_destroyed;
	if (engine->callbacks.func_on_engine_destroy) {
		engine->callbacks.func_on_engine_destroy();
	}
	engine->on_destroy();
	singleton = nullptr;
	memdelete(engine);
	if (on_engine_destroyed) {
		on_engine_destroyed();
	}
}

SpxCallbackInfo *SpxEngine::get_callbacks() {
	return &callbacks;
}

// 生成进程内唯一对象 ID。直接调用方：SpxManager::get_unique_id()；顶层调用方：Go 创建对象 API。
GdInt SpxEngine::get_unique_id() {
	return global_id++;
}

Node *SpxEngine::get_spx_root() {
	return spx_root;
}

SceneTree *SpxEngine::get_tree() {
	return tree;
}

Window *SpxEngine::get_root() {
	if (tree == nullptr) {
		return nullptr;
	}
	return tree->get_root();
}

// 绑定 Godot 主循环与 SPX 场景锚点，并把延迟回调代理挂到根 Window。
// 直接调用方：Spx::on_start()；顶层调用方：Godot 主循环 start 阶段。
// Godot 规定：Node 必须在主线程 add_child；加入树后其所有权和通知由 SceneTree 管理。
void SpxEngine::set_root_node(SceneTree *p_tree, Node *p_node) {
	tree = p_tree;
	spx_root = p_node;
	if (tree != nullptr && tree->get_root() != nullptr && delay_proxy == nullptr) {
		delay_proxy = memnew(SpxCallbackProxy);
		tree->get_root()->add_child(delay_proxy);
		on_timeout_callable = Callable(delay_proxy, "_on_timeout");
	}
}

// 唤醒并启动全部 C++ Manager，然后通知上层“SPX 引擎已经启动”。
// 最近调用方：Spx::on_start()。
// 最顶层入口：Godot SceneTree 主循环 start 阶段。
// 顺序很重要：Manager.on_start() 早于 callbacks.func_on_engine_start()。
void SpxEngine::on_awake() {
	if (has_exit || managers_awake) {
		return;
	}

	managers_awake = true;
	_notify_managers(&SpxManager::on_awake);
	_notify_managers(&SpxManager::on_start);

	if (callbacks.func_on_engine_start) {
		callbacks.func_on_engine_start();
	}
}

// 最近调用方：Spx::on_fixed_update()；最顶层来源：Godot 物理帧。
// 先更新 C++ Managers，再通过 Web JS/Native ABI 回调 Go。
void SpxEngine::on_fixed_update(float delta) {
	if (has_exit) {
		return;
	}

	if (is_spx_paused && !should_execute_single_frame) {
		return;
	}

	_notify_managers(&SpxManager::on_fixed_update, delta);

	if (callbacks.func_on_engine_fixed_update) {
		callbacks.func_on_engine_fixed_update(delta);
	}
}

// 最近调用方：Spx::on_update()；最顶层来源：Godot 每帧主循环。
// 顺序为 C++ Managers -> Go 回调 -> 画笔命令 flush。
void SpxEngine::on_update(float delta) {
	if (has_exit) {
		return;
	}

	if (is_spx_paused && !should_execute_single_frame) {
		return;
	}

	if (should_execute_single_frame) {
		should_execute_single_frame = false;
	}

	_notify_managers(&SpxManager::on_update, delta);

	if (callbacks.func_on_engine_update) {
		callbacks.func_on_engine_update(delta);
	}

	if (pen) {
		pen->flush_all();
	}

	if (is_spx_paused && tree && !tree->is_paused()) {
		tree->set_pause(true);
	}
}

// 断开定时器、释放场景辅助节点并逆序销毁 Managers。
// 直接调用方：shutdown()；顶层调用方：Godot 主循环 destroy/模块反初始化。
// Godot 规定：已挂入 SceneTree 的 Node 使用 queue_free() 延迟到安全时机释放。
void SpxEngine::on_destroy() {
	_disconnect_reset_timer();
	clear_frozen_frame();

	if (managers_awake) {
		_notify_managers(&SpxManager::on_destroy);
		managers_awake = false;
	}

	if (delay_proxy) {
		delay_proxy->clear_callback();
		delay_proxy->queue_free();
		delay_proxy = nullptr;
	}
	on_timeout_callable = Callable();

	callbacks = get_default_spx_callbacks();
	on_runtime_panic = nullptr;
	on_runtime_exit = nullptr;
	on_runtime_reset = nullptr;
	_destroy_all_managers();
	tree = nullptr;
	spx_root = nullptr;
}

// 进入不可恢复的进程退出状态并停止常规 runtime 回调。
// 直接调用方：SpxExtMgr::request_exit()；顶层调用方：Go runtime RequestExit。
void SpxEngine::on_exit(int exit_code) {
	if (has_exit) {
		return;
	}

	has_exit = true;

	_notify_managers(&SpxManager::on_exit, exit_code);

	// 立即停止普通 runtime 事件；最终 shutdown 仍保留并负责 destroy/destroyed 两个收尾回调。
	const auto on_engine_destroy = callbacks.func_on_engine_destroy;
	const auto on_engine_destroyed = callbacks.func_on_engine_destroyed;
	callbacks = get_default_spx_callbacks();
	callbacks.func_on_engine_destroy = on_engine_destroy;
	callbacks.func_on_engine_destroyed = on_engine_destroyed;
}

// 重置一局 SPX 游戏但保留 Godot 进程/WASM，便于 Web 下一局复用底层引擎。
// 最近调用方：Spx::reset()；最顶层入口：Go RequestExit -> ExtMgr.RequestReset，或页面异常恢复。
void SpxEngine::on_reset(int reset_code) {
	if (is_spx_reset) {
		return;
	}

	is_spx_reset = true;
	capture_last_frame();
	_do_reset(reset_code);
}

bool SpxEngine::is_reset() {
	return is_spx_reset;
}

// 从 reset 状态恢复各 Manager。
// 最近调用方：Spx::restart()；最顶层入口：GameApp.startGame()。
void SpxEngine::restart() {
	if (!is_spx_reset) {
		return;
	}

	_disconnect_reset_timer();
	clear_frozen_frame();
	_set_paused_pure(false);
	is_spx_reset = false;
#ifdef WEB_ENABLED
	godot_js_spx_contact_session_start();
#endif

	_notify_managers(&SpxManager::on_start);
}

void SpxEngine::set_delay_runtime_reset(bool p_delay) {
	// 当前生产代码暂无直接调用方，作为平台启动层的策略开关保留；顶层需求是 Web 重置末帧展示。
	should_delay_runtime_reset = p_delay;
}

// 把当前 Viewport 截图覆盖到独立 CanvasLayer，避免 Web 重置期间画面闪空。
// 直接调用方：on_reset()；顶层调用方：Go runtime reset/退出请求。
void SpxEngine::capture_last_frame() {
	if (is_frozen_frame || !tree) {
		return;
	}

	Ref<Image> img = _get_viewport_image();
	if (img.is_null()) {
		return;
	}

	freeze_screen = _create_freeze_texture(img);
	_attach_freeze_node(freeze_screen);
	is_frozen_frame = true;
}

// 移除重置等待期的截图节点。
// 直接调用方：restart()/on_destroy()；顶层调用方：新一局启动或 Godot 退出。
// queue_free() 是 Godot 对树内 Node 的安全释放方式，实际删除在当前帧末尾发生。
void SpxEngine::clear_frozen_frame() {
	if (!is_frozen_frame) {
		return;
	}

	if (freeze_screen) {
		freeze_screen->queue_free();
		freeze_screen = nullptr;
	}

	if (freeze_layer) {
		freeze_layer->queue_free();
		freeze_layer = nullptr;
	}

	is_frozen_frame = false;
}

// 暂停 Godot SceneTree 并同步 SPX 状态。
// 直接调用方：Spx::pause()；顶层调用方：Go engine 暂停 API；必须在 Godot 主线程执行。
void SpxEngine::pause() {
	ERR_FAIL_COND(!Thread::is_main_thread());
	if (tree) {
		tree->set_pause(true);
		_on_godot_pause_changed(true);
	}
}

// 恢复 Godot SceneTree 并通知各 Manager。
// 直接调用方：Spx::resume()；顶层调用方：Go engine 恢复 API；必须在 Godot 主线程执行。
void SpxEngine::resume() {
	ERR_FAIL_COND(!Thread::is_main_thread());
	if (tree) {
		tree->set_pause(false);
		_on_godot_pause_changed(false);
	}
}

bool SpxEngine::is_paused() const {
	return is_spx_paused;
}

// 暂时解除 SceneTree 暂停，让下一个主循环帧通过，随后 on_update() 清除单帧标记。
// 直接调用方：Spx::next_frame()；顶层调用方：Go 调试单步 API；必须在主线程执行。
void SpxEngine::next_frame() {
	ERR_FAIL_COND(!Thread::is_main_thread());
	if (is_spx_paused && tree) {
		tree->set_pause(false);
		should_execute_single_frame = true;
	}
}

// 执行本局重置：先回调 Go 清理游戏对象，再清理 Managers，最后通知宿主完成重置。
// 直接调用方：on_reset()；顶层调用方：Go runtime reset/退出请求。
void SpxEngine::_do_reset(int reset_code) {
	if (callbacks.func_on_engine_reset) {
		callbacks.func_on_engine_reset();
	}

	_notify_managers(&SpxManager::on_reset, reset_code);

	if (should_delay_runtime_reset) {
		_invoke_runtime_reset_delayed(reset_code);
	} else {
		_invoke_runtime_reset(reset_code);
	}
}

// 立即暂停场景树并同步通知宿主 reset 完成。
// 直接调用方：_do_reset() 或延迟 timer lambda；顶层调用方：Go/Web 重置流程。
void SpxEngine::_invoke_runtime_reset(int reset_code) {
	_set_paused_pure(true);
	auto callback = get_on_runtime_reset();
	if (callback) {
		callback(reset_code);
	}
}

// 使用 Godot SceneTreeTimer 延迟 reset 完成通知，使冻结末帧能在浏览器中保持一段时间。
// 直接调用方：_do_reset()；顶层调用方：启用了延迟策略的 Web 重置流程。
// Godot 规则：signal 的 Callable 目标必须是仍然存活的 Object，因此由树内 delay_proxy 承接。
void SpxEngine::_invoke_runtime_reset_delayed(int reset_code) {
	if (!tree || !delay_proxy) {
		return;
	}

	_disconnect_reset_timer();
	reset_timer = tree->create_timer(RESET_PAUSE_DELAY_SEC);
	delay_proxy->set_callback([this, reset_code]() {
		_invoke_runtime_reset(reset_code);
	});
	reset_timer->connect("timeout", on_timeout_callable);
}

// 主动断开 timer signal 并释放捕获，避免销毁后 Callable 再进入已失效的 SpxEngine。
// 直接调用方：restart()/on_destroy()/_invoke_runtime_reset_delayed()。
void SpxEngine::_disconnect_reset_timer() {
	if (reset_timer.is_valid() && reset_timer->is_connected("timeout", on_timeout_callable)) {
		reset_timer->disconnect("timeout", on_timeout_callable);
	}
	reset_timer.unref();
	if (delay_proxy != nullptr) {
		delay_proxy->clear_callback();
	}
}

// 统一处理 Godot pause 状态变化并广播到邮箱、Managers 和 Go 回调。
// 直接调用方：pause()/resume()；顶层调用方：Go engine 控制 API。
void SpxEngine::_on_godot_pause_changed(bool is_godot_paused) {
	if (is_godot_paused != is_spx_paused) {
		is_spx_paused = is_godot_paused;
		Spx::pending_controls.set_paused(is_spx_paused);

		_notify_managers(is_spx_paused ? &SpxManager::on_pause : &SpxManager::on_resume);

		if (callbacks.func_on_engine_pause) {
			callbacks.func_on_engine_pause(is_spx_paused);
		}
	}
}

// 只同步底层暂停状态，不发 Manager/Go pause 回调，供 reset/restart 内部状态切换使用。
// 直接调用方：restart()/_invoke_runtime_reset()；必须在 Godot 主线程执行。
void SpxEngine::_set_paused_pure(bool p_paused) {
	ERR_FAIL_COND(!Thread::is_main_thread());
	if (tree) {
		tree->set_pause(p_paused);
	}
	is_spx_paused = p_paused;
	Spx::pending_controls.set_paused(p_paused);
}

// 从根 Viewport 读取当前渲染结果；直接调用方：capture_last_frame()。
// Godot 规则：窗口不可绘制时 ViewportTexture 可能没有可用图像，调用方需接受空 Ref。
Ref<Image> SpxEngine::_get_viewport_image() const {
	Viewport *vp = tree->get_root();
	if (!vp) {
		return Ref<Image>();
	}

	DisplayServer *display_server = DisplayServer::get_singleton();
	if (!display_server || !display_server->window_can_draw()) {
		return Ref<Image>();
	}

	return vp->get_texture()->get_image();
}

// 根据截图构造铺满 Viewport 的 TextureRect；直接调用方：capture_last_frame()。
TextureRect *SpxEngine::_create_freeze_texture(const Ref<Image> &img) const {
	Ref<ImageTexture> tex = ImageTexture::create_from_image(img);
	TextureRect *screen = memnew(TextureRect);
	screen->set_texture(tex);
	screen->set_stretch_mode(TextureRect::STRETCH_SCALE);
	screen->set_anchors_and_offsets_preset(Control::PRESET_FULL_RECT);
	screen->set_mouse_filter(Control::MOUSE_FILTER_IGNORE);
	return screen;
}

// 把冻结画面挂入根 Viewport；直接调用方：capture_last_frame()。
// add_child 后节点归 SceneTree 生命周期管理，后续只通过 queue_free() 请求删除。
void SpxEngine::_attach_freeze_node(TextureRect *screen) {
	Viewport *vp = tree->get_root();
	if (!vp || !screen) {
		return;
	}

	if (!freeze_layer) {
		freeze_layer = memnew(CanvasLayer);
		freeze_layer->set_layer(1);
		vp->add_child(freeze_layer);
	}

	freeze_layer->add_child(screen);
}

// 按依赖顺序创建所有 C++ Manager。
// 直接调用方：register_callbacks()；顶层调用方：Native gdspx_init()/Web 模块 CORE 初始化。
void SpxEngine::_initialize_managers() {
	input = _create_manager<SpxInputMgr>();
	audio = _create_manager<SpxAudioMgr>();
	physics = _create_manager<SpxPhysicsMgr>();

	sprite = _create_manager<SpxSpriteMgr>();
	ui = _create_manager<SpxUiMgr>();
	scene = _create_manager<SpxSceneMgr>();
	camera = _create_manager<SpxCameraMgr>();

	platform = _create_manager<SpxPlatformMgr>();
	res = _create_manager<SpxResMgr>();
	debug = _create_manager<SpxDebugMgr>();

	navigation = _create_manager<SpxNavigationMgr>();
	pen = _create_manager<SpxPenMgr>();
	tilemap = _create_manager<SpxTilemapMgr>();
	tilemapparser = _create_manager<SpxTilemapparserMgr>();
}

// 按创建逆序释放 Managers，并清空全部非拥有型快捷指针。
// 直接调用方：on_destroy()；顶层调用方：Godot 主循环 destroy/模块反初始化。
void SpxEngine::_destroy_all_managers() {
	for (int i = managers.size() - 1; i >= 0; --i) {
		memdelete(managers[i]);
	}
	managers.clear();

	input = nullptr;
	audio = nullptr;
	physics = nullptr;
	sprite = nullptr;
	ui = nullptr;
	scene = nullptr;
	camera = nullptr;
	platform = nullptr;
	res = nullptr;
	debug = nullptr;
	navigation = nullptr;
	pen = nullptr;
	tilemap = nullptr;
	tilemapparser = nullptr;
}
