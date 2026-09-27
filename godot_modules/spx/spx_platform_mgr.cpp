/**************************************************************************/
/*  spx_platform_mgr.cpp                                                     */
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

#include "spx_platform_mgr.h"

#include "core/config/engine.h"
#include "core/os/thread.h"
#include "scene/main/window.h"

#include "spx.h"
#include "spx_camera_mgr.h"
#include "spx_engine.h"

#ifdef WEB_ENABLED
#include "web/spx_web_bridge.h"
#endif

#ifdef MACOS_ENABLED

// Godot macOS 后端按屏幕最大缩放因子处理原生窗口尺寸；SPX 对 Go 暴露逻辑尺寸，
// 因此 set/get 必须成对缩放，避免 Retina 屏幕窗口大小翻倍或减半。
static Size2i _spx_scale_window_size_for_macos(const Size2i &p_size) {
	const float scale = DisplayServer::get_singleton()->screen_get_max_scale();
	return Size2i((Vector2(p_size) * scale).round());
}

static Size2i _spx_unscale_window_size_for_macos(const Size2i &p_size) {
	const float scale = DisplayServer::get_singleton()->screen_get_max_scale();
	return Size2i((Vector2(p_size) / scale).round());
}
#endif

void SpxPlatformMgr::on_awake() {
	// 直接调用方：SpxEngine::_notify_managers(on_awake)；顶层为 Godot/SPX 启动。
	// get_user_data_dir 是 Godot 对 user:// 的平台化解析结果。
	persistent_data_dir = ::OS::get_singleton()->get_user_data_dir();
}

void SpxPlatformMgr::on_reset(int reset_code) {
	// 直接调用方：SpxEngine::on_reset；顶层为 Go runtime 重置/重跑游戏。
	window_size_uses_content_scale = false;
	set_stretch(false, 0, 0);
}

void SpxPlatformMgr::set_stretch(GdBool enabled, GdInt content_width, GdInt content_height) {
	// 直接调用方：生成的 ABI；顶层为 Go 项目窗口/舞台配置。
	auto root = get_root();
	if (root == nullptr) {
		return;
	}

	auto target_mode = enabled ? Window::CONTENT_SCALE_MODE_CANVAS_ITEMS : Window::CONTENT_SCALE_MODE_DISABLED;
	if (root->get_content_scale_mode() != target_mode) {
		root->set_content_scale_mode(target_mode);
	}

#ifdef WEB_ENABLED
	if (enabled) {
		// Web 调用方已应用窗口和设备像素缩放，这里只写逻辑内容尺寸。
		root->set_content_scale_size(Size2i(content_width, content_height));
	}
#endif
	auto target_aspect = enabled ? Window::CONTENT_SCALE_ASPECT_KEEP : Window::CONTENT_SCALE_ASPECT_IGNORE;
	if (root->get_content_scale_aspect() != target_aspect) {
		root->set_content_scale_aspect(target_aspect);
	}
}

void SpxPlatformMgr::set_window_position(GdVec2 pos) {
	DisplayServer::get_singleton()->window_set_position(Size2i(pos.x, pos.y));
}
GdVec2 SpxPlatformMgr::get_window_position() {
	auto pos = DisplayServer::get_singleton()->window_get_position();
	return GdVec2(pos.x, pos.y);
}
void SpxPlatformMgr::set_window_size(GdInt width, GdInt height, GdBool with_content_scale) {
	if (with_content_scale) {
		get_root()->set_content_scale_size(Size2i(width, height));
	}

	window_size_uses_content_scale = with_content_scale;

	Size2i window_size(width, height);
#ifdef MACOS_ENABLED
	if (window_size_uses_content_scale) {
		// SPX 调用方传逻辑窗口尺寸，显式转换后可在 HiDPI 屏幕保持可见尺寸稳定。
		window_size = _spx_scale_window_size_for_macos(window_size);
	}
#endif

	get_root()->set_size(window_size);
}

GdVec2 SpxPlatformMgr::get_window_size() {
#ifdef WEB_ENABLED
	auto size = spx_web_get_window_size();
#else
	auto size = DisplayServer::get_singleton()->window_get_size();
#endif
#ifdef MACOS_ENABLED
	if (window_size_uses_content_scale) {
		size = _spx_unscale_window_size_for_macos(size);
	}
#endif
	return GdVec2(size.x, size.y);
}

void SpxPlatformMgr::set_window_title(GdString title) {
	DisplayServer::get_singleton()->window_set_title(SpxStr(title));
}

GdString SpxPlatformMgr::get_window_title() {
	String title = "";
	return SpxReturnStr(title);
}

void SpxPlatformMgr::set_window_fullscreen(GdBool enable) {
	auto mode = enable ? DisplayServer::WINDOW_MODE_FULLSCREEN : DisplayServer::WINDOW_MODE_WINDOWED;
	DisplayServer::get_singleton()->window_set_mode(mode);
}

GdBool SpxPlatformMgr::is_window_fullscreen() {
	return get_root()->get_mode() == Window::MODE_FULLSCREEN;
}

void SpxPlatformMgr::set_debug_mode(GdBool enable) {
	Spx::set_debug_mode(enable);
}

GdBool SpxPlatformMgr::is_debug_mode() {
	return Spx::is_debug_mode();
}

GdBool SpxPlatformMgr::is_main_thread() {
	// Go 侧用此查询决定是否需要把 Godot 对象操作投递到主线程。
	return Thread::is_main_thread();
}

void SpxPlatformMgr::set_time_scale(GdFloat time_scale) {
	Engine::get_singleton()->set_time_scale(time_scale);
}

GdFloat SpxPlatformMgr::get_time_scale() {
	return Engine::get_singleton()->get_time_scale();
}

void SpxPlatformMgr::set_max_fps(GdInt fps) {
	Engine::get_singleton()->set_max_fps(fps);
}

GdInt SpxPlatformMgr::get_max_fps() {
	return Engine::get_singleton()->get_max_fps();
}

GdString SpxPlatformMgr::get_persistent_data_dir() {
	auto value = _get_persistent_data_dir();
	return SpxReturnStr(value);
}

String SpxPlatformMgr::_get_persistent_data_dir() {
	return persistent_data_dir;
}

void SpxPlatformMgr::_set_persistent_data_dir(String path) {
	persistent_data_dir = path;
}
void SpxPlatformMgr::set_persistent_data_dir(GdString path) {
	auto path_str = SpxStr(path);
	_set_persistent_data_dir(path_str);
}

GdBool SpxPlatformMgr::is_in_persistent_data_dir(GdString path) {
	auto path_str = SpxStr(path);
	return path_str.begins_with(persistent_data_dir);
}
