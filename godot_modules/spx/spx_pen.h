/**************************************************************************/
/*  spx_pen.h                                                             */
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

#ifndef SPX_PEN_H
#define SPX_PEN_H

#include "gdextension_spx_ext.h"
#include "spx_abi.h"
#include "spx_mgr_access.h"
#include "scene/resources/texture.h"

class Node;

class SpxSprite;
class SpxPenSurface;

// 一支 Go/SPX 画笔在 Godot 侧的状态对象。
// 本类不是 Node：SpxPenMgr 负责分配和销毁，surface 只借用 Manager 创建的共享画布。
// 直接调用方：SpxPenMgr 的 SPX_BIND 接口和逐帧 on_update()。
// 顶层调用方：Go Pen API -> PenSyncBuffer/engine Manager -> Native 或 Web ABI -> SpxPenMgr。
class SpxPen {
private:
	GdObj id; // Go 侧对象句柄，由 SpxEngine 分配，只用于跨语言定位对象。
	SpxPenSurface *surface = nullptr; // 借用共享画布；挂树后由父节点/SceneTree 持有。
	Vector2 last_draw_pos; // 上一个真正提交到画布的 Godot 坐标点。
	bool has_last_draw_pos = false; // last_draw_pos 当前是否可用于连接下一条线段。
	bool needs_start_cap = true; // 下一段是否需要补画 Scratch 风格圆形起点端帽。
	bool is_pen_down = false; // 画笔是否处于落笔状态。
	float min_draw_distance = 1.0f; // 相邻采样点的最小像素距离，避免生成退化线段。

	// 画笔的逻辑样式。颜色调整沿用 Scratch 的色彩/亮度/透明度语义。
	struct PenProperties {
		Color color = Color(0, 0, 0, 1); // 基础颜色，默认黑色。
		float size = 2.0f; // 笔迹直径，单位为舞台像素。
		float saturation = 1.0f; // 基于基础颜色的饱和度倍率。
		float brightness = 1.0f; // 基于基础颜色的明度倍率。
		float transparency = 0.0f; // 兼容 SPX 属性存储的透明度状态。
	} pen_properties; // 当前样式，仅在 Godot 主线程读写。

	Vector2 current_pen_pos; // 当前笔尖的 Godot 坐标；SPX 坐标在 Manager 边界已转换。
	bool move_by_mouse = false; // true 时 on_update() 每帧从 Godot Input 读取鼠标位置。

	Ref<Texture2D> stamp_texture; // 当前印章纹理；Ref 按 Godot 引用计数规则持有资源。
	String stamp_texture_path; // 与 stamp_texture 对应的 SPX 资源路径，用于避免重复加载。

private:
	void _draw_line(GdVec2 from, GdVec2 to, float size, Color color, bool draw_start_cap);
	GdVec2 _get_draw_position(GdVec2 position, float size) const;
	void _start_new_line();
	void _append_current_point_if_needed(GdVec2 position);
	Color _get_current_color() const;
	void _stamp_texture(const Ref<Texture2D> &texture, GdVec2 position, GdFloat rotation_radians, GdVec2 scale);
	Ref<Texture2D> _resolve_stamp_texture(const String &texture_path);

public:
	// 生命周期由 SpxObjectMgr 驱动；顶层均来自 SpxEngine 的 awake/update/reset/destroy。
	void on_create(GdInt p_id, Node *p_root);
	void on_destroy();
	void on_update(float delta);
	void on_reset(int reset_code);

public:
	// 画笔操作的直接调用方是 SpxPenMgr；顶层调用方是 Go 侧 Pen/Stamp API。
	void on_erase_all();
	GdObj get_id();
	void on_down(GdBool move_by_mouse);
	void on_up();
	void stamp();
	void move_to(GdVec2 position);
	void set_color_to(GdColor color);
	void change_by(GdInt property, GdFloat amount);
	void set_to(GdInt property, GdFloat value);
	void change_size_by(GdFloat amount);
	void set_size_to(GdFloat size);
	void set_stamp_texture(GdString texture_path);
	// Godot Transform2D 的旋转量使用弧度，因此此参数不再做角度制转换。
	void stamp_with_transform(GdString texture_path, GdVec2 position, GdFloat rotation_radians, GdVec2 scale);
};

#endif // SPX_PEN_H
