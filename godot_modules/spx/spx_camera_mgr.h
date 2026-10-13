/**************************************************************************/
/*  spx_camera_mgr.h                                                      */
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

#ifndef SPX_CAMERA_MGR_H
#define SPX_CAMERA_MGR_H

#include "gdextension_spx_ext.h"
#include "spx_manager.h"

class Camera2D;

// SPX 对 Camera2D/Viewport 的适配层，统一相机位置、缩放、舞台边界和鼠标世界坐标，
// 并在 SPX（Y 轴向上）与 Godot（Y 轴向下）坐标之间转换。
// 直接调用方：生成的 camera ABI、SpxInputMgr、SpxPhysicsMgr、渲染和分层模块；
// 顶层调用方：Go CameraMgr、输入查询、边缘检测与每帧可见性排序。
// Camera2D 和 Viewport 属于场景树对象，所有访问限定在 Godot 主线程。
class SpxCameraMgr : public SpxManager {

private:
	// 当前使用的 Camera2D 非拥有裸指针；可能来自用户场景，也可能由本类创建。
	Camera2D *camera = nullptr;
	// true 表示 camera 由本类 memnew，destroy 时负责 queue_free；场景相机则不释放。
	bool owns_camera = false;

public:
	// 直接调用方：SpxEngine 生命周期分发；顶层来源：Godot 启动或 Go reset/退出。
	void on_awake() override;
	void on_destroy() override;
	void on_reset(int reset_code) override;
	// 内部直接调用方：音频和渲染模块；返回值仅在主线程且 Manager 存活期有效。
	Camera2D *get_camera() { return camera; }
	Vector2 get_global_mouse_position();
	void set_stretch_clear_color();

public:
	// 以下 SPX_BIND 由 ABI 直接调用，顶层为 Go CameraMgr/舞台配置 API。
	SPX_BIND GdVec2 get_camera_position();
	SPX_BIND void set_camera_position(GdVec2 position);
	SPX_BIND GdVec2 get_camera_zoom();
	SPX_BIND void set_camera_zoom(GdVec2 size);
	SPX_BIND GdRect2 get_viewport_rect();
	SPX_BIND GdRect2 get_global_camera_rect();
	SPX_BIND GdRect2 get_stage_limits_rect();
	SPX_BIND void set_camera_limit(GdInt side, GdInt limit);
	SPX_BIND void set_camera_smoothing(GdBool enabled);
};

#endif // SPX_CAMERA_MGR_H
