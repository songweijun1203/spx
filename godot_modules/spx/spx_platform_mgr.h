/**************************************************************************/
/*  spx_platform_mgr.h                                                       */
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

#ifndef SPX_PLATFORM_MGR_H
#define SPX_PLATFORM_MGR_H

#include "gdextension_spx_ext.h"
#include "spx_manager.h"

// SPX 平台适配服务，屏蔽桌面/Web/macOS 在窗口尺寸、拉伸、持久化目录和
// 引擎全局参数上的差异。直接调用方是生成的 platform ABI 和 SpxResMgr；
// 顶层调用方为 Go PlatformMgr、项目启动配置、调试/帧率/存档相关 API。
// Window、DisplayServer、Engine 都是 Godot 全局或场景对象，除 is_main_thread
// 这类纯查询外，调用方必须把操作调度到 Godot 主线程。
class SpxPlatformMgr : public SpxManager {
	// 当前持久化根目录。默认取 Godot user:// 对应的真实目录，可被宿主覆盖。
	String persistent_data_dir = "res://";
	// 当前窗口尺寸是否按逻辑内容尺寸解释；macOS HiDPI 读写时据此缩放。
	bool window_size_uses_content_scale = false;

public:
	// 直接调用方：SpxEngine 生命周期分发；顶层来源：Godot 启动或 Go reset。
	void on_awake() override;
	void on_reset(int reset_code) override;
	void _set_persistent_data_dir(String path);
	String _get_persistent_data_dir();

public:
	// 开关 Godot 内容缩放并保持宽高比。直接调用方：ABI；顶层为 Go 项目窗口配置。
	// Godot 规则：content_scale_mode/aspect 属于 Window，必须在主线程更新。
	SPX_BIND void set_stretch(GdBool enabled, GdInt content_width, GdInt content_height);

	SPX_BIND void set_window_position(GdVec2 pos);
	SPX_BIND GdVec2 get_window_position();
	SPX_BIND void set_window_size(GdInt width, GdInt height, GdBool with_content_scale);
	SPX_BIND GdVec2 get_window_size();
	SPX_BIND void set_window_title(GdString title);
	SPX_BIND GdString get_window_title();
	SPX_BIND void set_window_fullscreen(GdBool enable);
	SPX_BIND GdBool is_window_fullscreen();
	SPX_BIND void set_debug_mode(GdBool enable);
	SPX_BIND GdBool is_debug_mode();
	SPX_BIND GdBool is_main_thread();

	SPX_BIND GdFloat get_time_scale();
	SPX_BIND void set_time_scale(GdFloat time_scale);

	SPX_BIND GdInt get_max_fps();
	SPX_BIND void set_max_fps(GdInt fps);

	SPX_BIND GdString get_persistent_data_dir();
	SPX_BIND void set_persistent_data_dir(GdString path);
	SPX_BIND GdBool is_in_persistent_data_dir(GdString path);
};

#endif // SPX_PLATFORM_MGR_H
