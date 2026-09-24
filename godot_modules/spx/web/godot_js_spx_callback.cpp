#include "godot_js_spx.h"
#include "spx_web_bridge.h"

#include "core/extension/gdextension_interface.h"
#include "core/object/callable_method_pointer.h"
#include "core/os/thread.h"
#include "../spx.h"
#include "../spx_engine.h"
#include "../spx_res_mgr.h"

static void _spx_web_request_reset() {
	Spx::reset(0);
}

static void _spx_web_apply_game_data(const String &p_path, const Vector<String> &p_file_paths) {
	SpxEngine *engine = SpxEngine::get_singleton();
	if (engine != nullptr && engine->get_res() != nullptr) {
		engine->get_res()->set_game_datas(p_path, p_file_paths);
	}
}

static void _spx_web_game_data_callback(const char *p_path, const char **p_file_paths, int p_size) {
	String path = String::utf8(p_path);
	Vector<String> file_paths;
	for (int i = 0; i < p_size; i++) {
		file_paths.push_back(String::utf8(p_file_paths[i]));
	}

#ifdef PROXY_TO_PTHREAD_ENABLED
	if (!Thread::is_main_thread()) {
		callable_mp_static(_spx_web_apply_game_data).bind(path, file_paths).call_deferred();
		return;
	}
#endif
	_spx_web_apply_game_data(path, file_paths);
}

// 建立 Web 版 Godot C++ -> JavaScript 回调表，并创建 SpxEngine 单例。
// 最近调用方：initialize_spx_module(MODULE_INITIALIZATION_LEVEL_CORE)。
// 最顶层入口：engine.js -> Module.callMain() -> Godot 模块 CORE 初始化。
// 这里不会加载 ispx.wasm；回调真正触发后由 library_godot_gdspx.js 再转发给 Go。
void spx_web_register_callbacks() {
	SpxCallbackInfo callback_infos = {};
	// 引擎生命周期、精灵、输入、碰撞和 UI 事件统一绑定到 Emscripten JS Library 导入函数。
	callback_infos.func_on_engine_start = &godot_js_spx_on_engine_start;
	callback_infos.func_on_engine_update = &godot_js_spx_on_engine_update;
	callback_infos.func_on_engine_fixed_update = &godot_js_spx_on_engine_fixed_update;
	callback_infos.func_on_engine_destroy = &godot_js_spx_on_engine_destroy;
	callback_infos.func_on_engine_destroyed = &godot_js_spx_on_engine_destroyed;
	callback_infos.func_on_engine_reset = &godot_js_spx_on_engine_reset;
	callback_infos.func_on_engine_pause = &godot_js_spx_on_engine_pause;
	callback_infos.func_on_scene_sprite_instantiated = &godot_js_spx_on_scene_sprite_instantiated;
	callback_infos.func_on_sprite_ready = &godot_js_spx_on_sprite_ready;
	callback_infos.func_on_sprite_updated = &godot_js_spx_on_sprite_updated;
	callback_infos.func_on_sprite_fixed_updated = &godot_js_spx_on_sprite_fixed_updated;
	callback_infos.func_on_sprite_destroyed = &godot_js_spx_on_sprite_destroyed;
	callback_infos.func_on_sprite_frames_set_changed = &godot_js_spx_on_sprite_frames_set_changed;
	callback_infos.func_on_sprite_animation_changed = &godot_js_spx_on_sprite_animation_changed;
	callback_infos.func_on_sprite_frame_changed = &godot_js_spx_on_sprite_frame_changed;
	callback_infos.func_on_sprite_animation_looped = &godot_js_spx_on_sprite_animation_looped;
	callback_infos.func_on_sprite_animation_finished = &godot_js_spx_on_sprite_animation_finished;
	callback_infos.func_on_sprite_vfx_finished = &godot_js_spx_on_sprite_vfx_finished;
	callback_infos.func_on_sprite_screen_exited = &godot_js_spx_on_sprite_screen_exited;
	callback_infos.func_on_sprite_screen_entered = &godot_js_spx_on_sprite_screen_entered;
	callback_infos.func_on_mouse_pressed = &godot_js_spx_on_mouse_pressed;
	callback_infos.func_on_mouse_released = &godot_js_spx_on_mouse_released;
	callback_infos.func_on_key_pressed = &godot_js_spx_on_key_pressed;
	callback_infos.func_on_key_released = &godot_js_spx_on_key_released;
	callback_infos.func_on_action_pressed = &godot_js_spx_on_action_pressed;
	callback_infos.func_on_action_just_pressed = &godot_js_spx_on_action_just_pressed;
	callback_infos.func_on_action_just_released = &godot_js_spx_on_action_just_released;
	callback_infos.func_on_axis_changed = &godot_js_spx_on_axis_changed;
	callback_infos.func_on_collision_enter = &godot_js_spx_on_collision_enter;
	callback_infos.func_on_collision_stay = &godot_js_spx_on_collision_stay;
	callback_infos.func_on_collision_exit = &godot_js_spx_on_collision_exit;
	callback_infos.func_on_trigger_enter = &godot_js_spx_on_trigger_enter;
	callback_infos.func_on_trigger_stay = &godot_js_spx_on_trigger_stay;
	callback_infos.func_on_trigger_exit = &godot_js_spx_on_trigger_exit;
	callback_infos.func_on_ui_ready = &godot_js_spx_on_ui_ready;
	callback_infos.func_on_ui_updated = &godot_js_spx_on_ui_updated;
	callback_infos.func_on_ui_destroyed = &godot_js_spx_on_ui_destroyed;
	callback_infos.func_on_ui_pressed = &godot_js_spx_on_ui_pressed;
	callback_infos.func_on_ui_released = &godot_js_spx_on_ui_released;
	callback_infos.func_on_ui_hovered = &godot_js_spx_on_ui_hovered;
	callback_infos.func_on_ui_clicked = &godot_js_spx_on_ui_clicked;
	callback_infos.func_on_ui_toggle = &godot_js_spx_on_ui_toggle;
	callback_infos.func_on_ui_text_changed = &godot_js_spx_on_ui_text_changed;

	// register_callbacks() 创建全局唯一的 SpxEngine，并同步创建各 C++ Spx*Mgr。
	SpxEngine::register_callbacks(&callback_infos);
	SpxEngine::register_runtime_panic_callbacks(godot_js_spx_on_runtime_panic);
	SpxEngine::register_runtime_exit_callbacks(godot_js_spx_on_runtime_exit);
	SpxEngine::register_runtime_reset_callbacks(godot_js_spx_on_reset_done);

	godot_js_spx_request_reset_cb(&_spx_web_request_reset);
	godot_js_spx_game_data_cb(&_spx_web_game_data_callback);
}

Size2i spx_web_get_window_size() {
	int32_t width = 0;
	int32_t height = 0;
	godot_js_spx_window_size_get(&width, &height);
	return Size2i(width, height);
}
