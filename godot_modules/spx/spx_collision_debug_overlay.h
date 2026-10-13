/**************************************************************************/
/*  spx_collision_debug_overlay.h                                         */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
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

#ifndef SPX_COLLISION_DEBUG_OVERLAY_H
#define SPX_COLLISION_DEBUG_OVERLAY_H

#include "scene/2d/node_2d.h"

class CollisionShape2D;

// CollisionShape2D 的 SPX 运行时调试覆盖层。它在 Godot 原生碰撞调试不可见时
// 复用 Shape2D::draw 绘制形状，避免与编辑器/SceneTree 原生调试重复显示。
// 直接调用方：spx_ensure_collision_debug_overlay、Godot 通知和目标 draw 信号；
// 顶层调用方：SpxSpriteRender 的碰撞体配置、平台 debug mode 切换、Godot 渲染循环。
// Godot 规则：Node 由场景树拥有；queue_redraw 只安排下一绘制阶段，不能直接调用 _draw；
// 信号连接和场景树遍历必须在主线程完成。
class SpxCollisionDebugOverlay : public Node2D {
	GDCLASS(SpxCollisionDebugOverlay, Node2D);

	// 目标 CollisionShape2D 的 ObjectID；不取得所有权，目标释放后查询自然返回空。
	ObjectID target_id;
	// SPX 指定的调试颜色；禁用态颜色在绘制时派生。
	Color debug_color;
	// 业务层是否要求显示；还需同时满足 SPX debug mode 且原生调试未接管。
	bool requested_visible = false;

	CollisionShape2D *_get_target() const;
	bool _is_native_collision_debug_visible() const;
	void _sync_visibility(bool p_debug_mode);
	void _on_target_redrawn();
	void _draw_debug_shape();

protected:
	// 本类没有脚本绑定；空实现满足 GDCLASS 的 ClassDB 注册约定。
	static void _bind_methods() {}
	// Godot 引擎通知入口；处理入树、逐帧检查和 CanvasItem 绘制阶段。
	void _notification(int p_what);

public:
	// 直接调用方：spx_ensure_collision_debug_overlay；顶层为精灵碰撞体配置/克隆恢复。
	void configure(CollisionShape2D *p_target, const Color &p_color, bool p_visible);
	void set_debug_color(const Color &p_color);
	void set_requested_visible(bool p_visible);
	void sync_debug_mode(bool p_enabled) { _sync_visibility(p_enabled); }

	SpxCollisionDebugOverlay();
};

// 以下帮助函数由 SpxSpriteRender/本覆盖层直接调用，顶层为碰撞调试显示。
Color spx_collision_debug_shape_color(const Color &p_color, bool p_disabled);
Color spx_collision_debug_one_way_color(const Color &p_color, bool p_disabled);

SpxCollisionDebugOverlay *spx_find_collision_debug_overlay(CollisionShape2D *p_target);
SpxCollisionDebugOverlay *spx_ensure_collision_debug_overlay(CollisionShape2D *p_target, const Color &p_color, bool p_visible);
// 直接调用方：Spx::set_debug_mode；顶层调用方：Go PlatformMgr.SetDebugMode。
void spx_collision_debug_mode_changed(bool p_enabled);

#endif // SPX_COLLISION_DEBUG_OVERLAY_H
