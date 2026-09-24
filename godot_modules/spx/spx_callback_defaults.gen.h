// 由 SPX 代码生成器自动生成，请勿直接编辑。
// 本文件根据 gdextension_spx_ext.h 中的回调 typedef 和 SpxCallbackInfo 自动生成。
// 每个字段都会获得一个参数签名完全一致的空 lambda，供 Go 尚未注册真实回调时使用；
// 需要新增或修改回调时应修改 gdextension_spx_ext.h.tmpl，而不是编辑本文件。
#ifndef SPX_CALLBACK_DEFAULTS_GEN_H
#define SPX_CALLBACK_DEFAULTS_GEN_H

#include "gdextension_spx_ext.h"

// 构造一张可安全调用的默认回调表，避免生命周期早期、关闭期间或未连接 Go 时
// 因空函数指针而崩溃。注册真实回调后，SpxEngine 会用调用方提供的表替换它。
inline SpxCallbackInfo get_default_spx_callbacks() {
	// 先将整个结构清零，随后生成器为每一个已声明字段安装同签名空函数。
	SpxCallbackInfo callbacks = {};
	callbacks.func_on_engine_start = []() {};
	callbacks.func_on_engine_update = [](GdFloat) {};
	callbacks.func_on_engine_fixed_update = [](GdFloat) {};
	callbacks.func_on_engine_destroy = []() {};
	callbacks.func_on_engine_destroyed = []() {};
	callbacks.func_on_engine_reset = []() {};
	callbacks.func_on_engine_pause = [](GdBool) {};
	callbacks.func_on_scene_sprite_instantiated = [](GdObj, GdString) {};
	callbacks.func_on_sprite_ready = [](GdObj) {};
	callbacks.func_on_sprite_updated = [](GdFloat) {};
	callbacks.func_on_sprite_fixed_updated = [](GdFloat) {};
	callbacks.func_on_sprite_destroyed = [](GdObj) {};
	callbacks.func_on_sprite_frames_set_changed = [](GdObj) {};
	callbacks.func_on_sprite_animation_changed = [](GdObj) {};
	callbacks.func_on_sprite_frame_changed = [](GdObj) {};
	callbacks.func_on_sprite_animation_looped = [](GdObj) {};
	callbacks.func_on_sprite_animation_finished = [](GdObj) {};
	callbacks.func_on_sprite_vfx_finished = [](GdObj) {};
	callbacks.func_on_sprite_screen_exited = [](GdObj) {};
	callbacks.func_on_sprite_screen_entered = [](GdObj) {};
	callbacks.func_on_mouse_pressed = [](GdInt) {};
	callbacks.func_on_mouse_released = [](GdInt) {};
	callbacks.func_on_key_pressed = [](GdInt) {};
	callbacks.func_on_key_released = [](GdInt) {};
	callbacks.func_on_action_pressed = [](GdString) {};
	callbacks.func_on_action_just_pressed = [](GdString) {};
	callbacks.func_on_action_just_released = [](GdString) {};
	callbacks.func_on_axis_changed = [](GdString, GdFloat) {};
	callbacks.func_on_collision_enter = [](GdInt, GdInt) {};
	callbacks.func_on_collision_stay = [](GdInt, GdInt) {};
	callbacks.func_on_collision_exit = [](GdInt, GdInt) {};
	callbacks.func_on_trigger_enter = [](GdInt, GdInt) {};
	callbacks.func_on_trigger_stay = [](GdInt, GdInt) {};
	callbacks.func_on_trigger_exit = [](GdInt, GdInt) {};
	callbacks.func_on_ui_ready = [](GdObj) {};
	callbacks.func_on_ui_updated = [](GdObj) {};
	callbacks.func_on_ui_destroyed = [](GdObj) {};
	callbacks.func_on_ui_pressed = [](GdObj) {};
	callbacks.func_on_ui_released = [](GdObj) {};
	callbacks.func_on_ui_hovered = [](GdObj) {};
	callbacks.func_on_ui_clicked = [](GdObj) {};
	callbacks.func_on_ui_toggle = [](GdObj, GdBool) {};
	callbacks.func_on_ui_text_changed = [](GdObj, GdString) {};
	return callbacks;
}

#endif // SPX_CALLBACK_DEFAULTS_GEN_H
