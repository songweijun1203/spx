/**************************************************************************/
/*  spx_pen_mgr.h                                                         */
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

#ifndef SPX_PEN_MGR_H
#define SPX_PEN_MGR_H

#include "gdextension_spx_ext.h"
#include "spx_object_mgr.h"
#include "spx_pen.h"

class SpxPenSurface;
namespace SpxPixelQuery {
struct Snapshot;
}

// 画笔子系统的 C++ 门面：管理所有 SpxPen，并将它们汇聚到一个持久化画布。
// 直接调用方：生成的 spx_pen_* ABI 包装、SpxEngine Manager 生命周期和像素感知代码。
// 顶层调用方：Go Pen API/每帧画笔批处理，以及 Sprite TouchingColor 等感知 API。
// Godot 规则：Node 的增删和 SubViewport/RenderingServer 操作必须发生在引擎主线程。
class SpxPenMgr : public SpxObjectMgr<SpxPen> {
private:
	SpxPenSurface *surface = nullptr; // 借用共享画布；创建后由 SceneTree 持有，销毁随 root queue_free。

public:
	// 生命周期直接由 SpxEngine::_notify_managers 调用；顶层来自 Godot 主循环阶段回调。
	void on_awake() override;
	void on_update(float delta) override;
	void on_destroy() override;
	void on_reset(int reset_code) override;

	// SPX_BIND 接口直接由 Native/Web ABI 调用；顶层来自 Go engine.PenMgr。
	SPX_BIND void destroy_all_pens();
	SPX_BIND void set_canvas_size(GdInt width, GdInt height);
	void flush_all(); // 直接调用方：SpxEngine::on_update；将本帧命令提交给 RenderingServer。
	bool capture(const Rect2 &p_query_bounds, SpxPixelQuery::Snapshot &r_snapshot); // 直接调用方：像素感知合成流程。
	SPX_BIND GdObj create_pen();
	SPX_BIND void destroy_pen(GdObj obj);
	SPX_BIND void batch_update_commands(const float *buffer_data, int len);
	// 单笔操作由 ABI 或 batch_update_commands 转发，顶层均为 Go 侧画笔对象方法。
	SPX_BIND void pen_stamp(GdObj obj);
	SPX_BIND void pen_stamp_sprite(GdObj sprite_id);
	SPX_BIND void move_pen_to(GdObj obj, GdVec2 position);
	SPX_BIND void pen_down(GdObj obj, GdBool move_by_mouse);
	SPX_BIND void pen_up(GdObj obj);
	SPX_BIND void set_pen_color_to(GdObj obj, GdColor color);
	SPX_BIND void change_pen_by(GdObj obj, GdInt property, GdFloat amount);
	SPX_BIND void set_pen_to(GdObj obj, GdInt property, GdFloat value);
	SPX_BIND void change_pen_size_by(GdObj obj, GdFloat amount);
	SPX_BIND void set_pen_size_to(GdObj obj, GdFloat size);
	SPX_BIND void set_pen_stamp_texture(GdObj obj, GdString texture_path);
	// Godot Transform2D 使用弧度，因此跨语言参数在此明确保持 radians。
	SPX_BIND void pen_stamp_with_transform(GdObj obj, GdString texture_path, GdVec2 position, GdFloat rotation_radians, GdVec2 scale);
};

#endif // SPX_PEN_MGR_H
