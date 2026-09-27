/**************************************************************************/
/*  spx_ext_mgr.h                                                      */
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

#ifndef SPX_EXT_MGR_H
#define SPX_EXT_MGR_H

#include "gdextension_spx_ext.h"
#include "spx_abi.h"

// 导出给 Go/Native/Web 的静态扩展门面。
// 这里不保存实例状态，所有调用转发到 Spx/SpxEngine；SPX_BIND 由代码生成器导出为稳定 ABI。
// 直接调用方：生成的 C/C++/JS bridge；顶层调用方：Go runtime 控制 API。
class SpxExtMgr {
public:
	// 引擎生命周期 API。
	// 请求进程退出；会通知 Go、停止 Manager，并让 SceneTree 退出。
	SPX_BIND static void request_exit(GdInt exit_code);
	// 重置当前游戏但保留 Godot/WASM 进程，供下一局复用。
	SPX_BIND static void request_reset(GdInt exit_code);
	// 从 reset 状态恢复一局游戏。
	SPX_BIND static void request_restart();
	// 把 runtime panic 消息同步转发给宿主。
	SPX_BIND static void on_runtime_panic(GdString msg);
	// 暂停与单步 API。
	// 暂停/恢复/单步均最终要求在 Godot 主线程修改 SceneTree。
	SPX_BIND static void pause();
	SPX_BIND static void resume();
	SPX_BIND static GdBool is_paused();
	SPX_BIND static void next_frame();

	// 图层排序 API。
	// 设置精灵图层排序策略；模式值由 Go 与 C++ ABI 约定。
	SPX_BIND static void set_layer_sorter_mode(GdInt mode);
};

#endif // SPX_EXT_MGR_H
