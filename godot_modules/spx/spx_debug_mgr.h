/**************************************************************************/
/*  spx_debug_mgr.h                                                       */
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

#ifndef SPX_DEBUG_MGR_H
#define SPX_DEBUG_MGR_H

#include "gdextension_spx_ext.h"
#include "scene/2d/node_2d.h"
#include "spx_manager.h"

// 一帧调试图形的记录。实际 Node2D 由 debug_root/场景树拥有，本结构只暂存裸指针。
struct DebugShape {
	enum Type {
		CIRCLE,
		RECT,
		LINE
	};
	// 图形类型，便于后续扩展按类型清理或统计。
	Type type;
	// SPX 调试请求转换后的 Godot 世界坐标。
	GdVec2 position;
	// 矩形半尺寸（当前实现入表前已除以 2）。
	GdVec2 size;
	// 圆半径。
	GdFloat radius;
	// 线段终点的 Godot 世界坐标。
	GdVec2 to_position;
	// 绘制颜色。
	GdColor color;
	// 场景树拥有的 Line2D/Node2D；下一帧通过 queue_free 延迟删除。
	Node2D *node;
};

// 一帧即逝的调试绘制服务。Go 侧请求经 ABI 切到主线程后提交圆、矩形和线段，
// Manager 在 Godot 帧更新时统一清理上一帧节点。
// 直接调用方：生成的 spx_debug_* ABI 和 SpxEngine 生命周期分发；
// 顶层调用方：Go DebugMgr、物理/导航调试 API。场景树增删必须在主线程；
// Mutex 只串行化 update/destroy 的清理路径，不能让 Node API 变成线程安全。
class SpxDebugMgr : public SpxManager {
private:
	// 当前帧创建的调试图形记录；节点所有权属于 debug_root。
	Vector<DebugShape> debug_shapes;
	// 本类创建并挂到 SPX 根节点的容器；场景树拥有，destroy 时 queue_free。
	Node2D *debug_root;

	void _clear_debug_shapes();
	// 串行化逐帧清理与销毁清理；提交函数仍要求调用者处于 Godot 主线程。
	static Mutex lock;

public:
	// 直接调用方：SpxEngine::_notify_managers；顶层来源：Godot 帧循环或 Go reset/退出。
	void on_awake() override;
	void on_update(float delta) override;
	void on_destroy() override;
	void on_reset(int reset_code) override;

	// 以下 SPX_BIND 由 ABI 直接调用；顶层为 Go DebugMgr 的一帧调试绘制。
	SPX_BIND void debug_draw_circle(GdVec2 pos, GdFloat radius, GdColor color);
	SPX_BIND void debug_draw_rect(GdVec2 pos, GdVec2 size, GdColor color);
	SPX_BIND void debug_draw_line(GdVec2 from, GdVec2 to, GdColor color);
};

#endif // SPX_DEBUG_MGR_H
