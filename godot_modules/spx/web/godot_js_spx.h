#ifndef GODOT_JS_SPX_H
#define GODOT_JS_SPX_H

#include <stddef.h>
#include <stdint.h>

typedef float GdFloat;
typedef int64_t GdObj;
typedef const void *GdString;
typedef int64_t GdInt;
typedef uint8_t GdBool;

#ifdef __cplusplus
extern "C" {
#endif

// Web 回调导入表：Godot C++ 直接调用，Emscripten 在链接时用 JS Library 实现这些符号，
// JS 再转发给 Go WASM。顶层来源为 SpxEngine/各 Manager 的生命周期和事件回调。
// 参数只使用固定宽度标量或 ABI 句柄；不可在边界上传递 Godot C++ 对象。

// 运行时异常、退出与重置结果。
extern void godot_js_spx_on_runtime_panic(GdString msg);
extern void godot_js_spx_on_runtime_exit(GdInt code);
extern void godot_js_spx_on_reset_done(GdInt code);
// 引擎生命周期。直接调用方：SpxEngine；顶层调用方：Godot MainLoop。
extern void godot_js_spx_on_engine_start();
extern void godot_js_spx_on_engine_update(GdFloat delta);
extern void godot_js_spx_on_engine_fixed_update(GdFloat delta);
extern void godot_js_spx_on_engine_destroy();
extern void godot_js_spx_on_engine_destroyed();
extern void godot_js_spx_on_engine_reset();
extern void godot_js_spx_on_engine_pause(GdBool is_paused);

extern void godot_js_spx_on_scene_sprite_instantiated(GdObj obj,GdString type_name);

// 精灵生命周期、动画信号和可见区域事件。
extern void godot_js_spx_on_sprite_ready(GdObj obj);
extern void godot_js_spx_on_sprite_updated(GdFloat delta);
extern void godot_js_spx_on_sprite_fixed_updated(GdFloat delta);
extern void godot_js_spx_on_sprite_destroyed(GdObj obj);

extern void godot_js_spx_on_sprite_frames_set_changed(GdObj obj);
extern void godot_js_spx_on_sprite_animation_changed(GdObj obj);
extern void godot_js_spx_on_sprite_frame_changed(GdObj obj);
extern void godot_js_spx_on_sprite_animation_looped(GdObj obj);
extern void godot_js_spx_on_sprite_animation_finished(GdObj obj);

extern void godot_js_spx_on_sprite_vfx_finished(GdObj obj);

extern void godot_js_spx_on_sprite_screen_exited(GdObj obj);
extern void godot_js_spx_on_sprite_screen_entered(GdObj obj);

// 输入事件。直接调用方：SpxInputMgr；顶层调用方：Godot Viewport/InputMap。
extern void godot_js_spx_on_mouse_pressed(GdInt keyid);
extern void godot_js_spx_on_mouse_released(GdInt keyid);
extern void godot_js_spx_on_key_pressed(GdInt keyid);
extern void godot_js_spx_on_key_released(GdInt keyid);
extern void godot_js_spx_on_action_pressed(GdString action_name);
extern void godot_js_spx_on_action_just_pressed(GdString action_name);
extern void godot_js_spx_on_action_just_released(GdString action_name);
extern void godot_js_spx_on_axis_changed(GdString action_name, GdFloat value);

// 物理接触事件。直接调用方：碰撞/触发信号代理；顶层调用方：Godot PhysicsServer。
extern void godot_js_spx_on_collision_enter(GdInt self_id, GdInt other_id);
extern void godot_js_spx_on_collision_stay(GdInt self_id, GdInt other_id);
extern void godot_js_spx_on_collision_exit(GdInt self_id, GdInt other_id);
extern void godot_js_spx_on_trigger_enter(GdInt self_id, GdInt other_id);
extern void godot_js_spx_on_trigger_stay(GdInt self_id, GdInt other_id);
extern void godot_js_spx_on_trigger_exit(GdInt self_id, GdInt other_id);

// UI 生命周期和值变化事件。直接调用方：SpxUi/绑定控件；顶层调用方：Godot Control 信号。
extern void godot_js_spx_on_ui_ready(GdObj obj);
extern void godot_js_spx_on_ui_updated(GdObj obj);
extern void godot_js_spx_on_ui_destroyed(GdObj obj);

extern void godot_js_spx_on_ui_pressed(GdObj obj);
extern void godot_js_spx_on_ui_released(GdObj obj);
extern void godot_js_spx_on_ui_hovered(GdObj obj);
extern void godot_js_spx_on_ui_clicked(GdObj obj);
extern void godot_js_spx_on_ui_toggle(GdObj obj, GdBool is_on);
extern void godot_js_spx_on_ui_text_changed(GdObj obj, GdString text);

// JS 宿主向 C++ 注册反向回调，并提供浏览器窗口尺寸。
extern void godot_js_spx_request_reset_cb(void (*p_callback)());
extern void godot_js_spx_game_data_cb(void (*p_callback)(const char *p_path, const char **p_filev, int p_filec));
extern void godot_js_spx_window_size_get(int32_t *p_x, int32_t *p_y);

#ifdef __cplusplus
}
#endif


#endif // GODOT_JS_SPX_H
