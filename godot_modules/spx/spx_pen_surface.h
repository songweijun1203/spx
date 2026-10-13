/**************************************************************************/
/*  spx_pen_surface.h                                                     */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/**************************************************************************/
/* Copyright (c) 2014-present Godot Engine contributors (see AUTHORS.md). */
/* Copyright (c) 2007-2014 Juan Linietsky, Ariel Manzur.                  */
/*                                                                        */
/* Permission is hereby granted, free of charge, to any person obtaining  */
/* a copy of this software and associated documentation files (the        */
/* "Software"), to deal in the Software without restriction, including  */
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

#ifndef SPX_PEN_SURFACE_H
#define SPX_PEN_SURFACE_H

#include "core/templates/vector.h"
#include "scene/2d/node_2d.h"
#include "scene/2d/sprite_2d.h"
#include "scene/main/viewport.h"
#include "scene/resources/material.h"

class AnimatedSprite2D;
namespace SpxPixelQuery {
struct Snapshot;
}

// 将逻辑画笔命令转换为 RenderingServer CanvasItem 的提交节点。
// 直接调用方：SpxPenSurface；顶层调用方：Go Pen API -> SpxPenMgr -> SpxPenSurface。
// Godot 规则：RID 不受 SceneTree 自动管理，本类必须显式 free；Ref 资源则按引用计数保活。
class SpxPenCanvas : public Node2D {
	GDCLASS(SpxPenCanvas, Node2D);

private:
	struct DrawCommand {
		enum Type {
			LINE,
			STAMP,
		};

		Type type = LINE; // 命令种类，决定 submit() 走线段批处理还是纹理绘制。
		Vector2 from; // 线段起点，使用画布本地坐标。
		Vector2 to; // 线段终点，使用画布本地坐标。
		Color color; // 笔迹颜色或印章调制色。
		float width = 1.0f; // 笔迹宽度，印章命令不使用。
		bool draw_start_cap = true; // 是否为当前线段补画圆形起点端帽。
		Ref<Texture2D> texture; // 印章纹理；等待提交期间保持资源存活。
		Transform2D transform; // 印章相对画布的变换。
		Rect2 rect; // 印章目标矩形，包含 centered/flip 后的方向。
		Ref<Material> material; // 调用时冻结的材质副本，避免后续特效反向修改印章。
		TextureFilter texture_filter = TEXTURE_FILTER_PARENT_NODE; // 继承原精灵的过滤策略。
		TextureRepeat texture_repeat = TEXTURE_REPEAT_PARENT_NODE; // 继承原精灵的重复策略。
	};

	// 强引用资源与对应 RenderingServer 画布项保持完全相同的存活期。
	struct DrawItem {
		RID rid; // RenderingServer 画布项，必须在 _clear_draw_items() 中显式释放。
		Ref<Texture2D> texture; // 与 RID 同寿命的纹理强引用。
		Ref<Material> material; // 与 RID 同寿命的材质强引用。
	};

	Vector<DrawCommand> pending_commands; // 尚未提交到 RenderingServer 的有序命令。
	Vector<DrawItem> draw_items; // 已提交且构成当前持久画面的服务器对象。
	RID _create_draw_item(const Ref<Texture2D> &p_texture = Ref<Texture2D>(), const Ref<Material> &p_material = Ref<Material>());
	void _clear_draw_items();
	void _draw_line_batch(int p_begin, int p_end);

protected:
	static void _bind_methods();

public:
	void add_line(const Vector2 &p_from, const Vector2 &p_to, float p_width, const Color &p_color, bool p_draw_start_cap);
	void add_stamp(const Ref<Texture2D> &p_texture, const Vector2 &p_position, float p_rotation, const Vector2 &p_scale);
	void add_stamp(AnimatedSprite2D *p_sprite, const Transform2D &p_transform);
	void clear_commands();
	bool has_pending_commands() const { return !pending_commands.is_empty(); }
	void submit(bool p_append);
	~SpxPenCanvas();
};

// Scratch 风格的共享画笔图层。命令由渲染器栅格化到持久透明 SubViewport，
// 避免每帧在 CPU 修改整张 Image 再上传 GPU。
// 直接调用方：SpxPen/SpxPenMgr；顶层调用方：Go Pen API 和颜色/透明度感知 API。
// Godot 规则：SubViewport 的 CLEAR_MODE/UPDATE_MODE 决定内容保留与真正渲染时机；
// add_child 后节点归 SceneTree 所有，成员指针仅借用，不得手工 memdelete。
class SpxPenSurface : public Node2D {
	GDCLASS(SpxPenSurface, Node2D);

private:
	SubViewport *render_target = nullptr; // 借用的离屏渲染目标子节点，由 SceneTree 管理。
	SpxPenCanvas *canvas = nullptr; // 借用的命令画布子节点，由 render_target 管理。
	Size2i canvas_size; // 当前离屏纹理尺寸，单位为像素。
	Ref<Image> collision_image; // 最近一次感知读回的 CPU 快照；新绘制或清屏后失效。
	bool clear_requested = true; // 下一次 flush 是否先清空持久化目标。
	bool _is_render_pending() const;

protected:
	static void _bind_methods();

public:
	// initialize 直接由 SpxPenMgr::on_awake 调用；顶层来自 Godot 主循环 start。
	void initialize(const Size2i &p_size);
	void set_canvas_size(const Size2i &p_size);
	void draw_line(const Vector2 &p_from, const Vector2 &p_to, float p_width, const Color &p_color, bool p_draw_start_cap);
	void draw_stamp(const Ref<Texture2D> &p_texture, const Vector2 &p_position, float p_rotation, const Vector2 &p_scale);
	void draw_stamp(AnimatedSprite2D *p_sprite);
	void clear();
	void flush();
	// 仅当查询范围与画布重叠时同步 GPU 并读回像素，避免普通帧产生昂贵阻塞。
	// 直接调用方：SpxPenMgr::capture；顶层调用方：Go 侧颜色/透明度碰撞查询。
	bool capture(const Rect2 &p_query_bounds, SpxPixelQuery::Snapshot &r_snapshot);
};

#endif // SPX_PEN_SURFACE_H
