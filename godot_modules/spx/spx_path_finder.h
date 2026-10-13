/**************************************************************************/
/*  spx_path_finer.h                                                      */
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

#ifndef SPX_PATH_FINER_H
#define SPX_PATH_FINER_H

#include "core/math/a_star_grid_2d.h"
#include "core/object/object.h"
#include "gdextension_spx_ext.h"
#include "scene/2d/node_2d.h"

class TileMapLayer;
class Node;
class PathDebugDrawer;
class CollisionShape2D;

// SPX 的网格寻路核心。它封装 Godot AStarGrid2D，扫描场景碰撞体生成阻塞格，
// 并在 ABI 边界完成 SPX/Godot 坐标与数组格式转换。
// 直接调用方：SpxNavigationMgr，亦可由 Godot ClassDB 脚本接口直接调用；
// 顶层调用方：Go NavigationMgr/精灵寻路 API，或 Godot 调试脚本。
// Godot 规则：RefCounted 由 Ref<> 自动管理，不可手工 memdelete；但 drawer 是 Node，
// 由场景树拥有并 queue_free。场景扫描、AStar 修改和 Node 操作均限定主线程。
class SpxPathFinder : public RefCounted {
	GDCLASS(SpxPathFinder, RefCounted);

private:
	// 引用计数拥有的 Godot A* 网格资源，构造时 instantiate，reset 仅清空内容。
	Ref<AStarGrid2D> astar;
	// 是否用网格角点补充多边形相交判断；当前默认只检查格子中心。
	bool is_precise_check = false;
	// 场景树拥有的可视化节点裸指针；退出树时回调 clear_drawer 解除引用。
	PathDebugDrawer *drawer = nullptr;

	// 世界/格子换算缓存；与 astar->cell_size 保持一致。
	Vector2 cached_cell_size{ 16, 16 };

protected:
	// Godot ClassDB 注册回调，在类型注册阶段调用；使部分方法可从脚本/反射访问。
	static void _bind_methods();

public:
	SpxPathFinder();
	~SpxPathFinder();

	// setup_spx 直接调用方：SpxNavigationMgr；顶层为 Go SetupPathFinder*。
	// setup 同时是 ClassDB 暴露接口；会扫描当前场景并可创建调试 Node。
	void setup_spx(GdVec2 size, GdVec2 cell_size, GdBool with_debug);
	void setup(Vector2i size, Vector2i cell_size, bool with_debug = false);

	void set_jumping_enabled(bool p_enabled);
	void add_all_obstacles(Node *root);

	void set_sprite_obstacle(GdObj obj, bool enabled);

	// find_path_spx 直接调用方：SpxNavigationMgr；顶层为 Go FindPath。
	// find_path 是 Godot 坐标版本，也由 PathDebugDrawer 和脚本接口调用。
	GdArray find_path_spx(GdVec2 p_from, GdVec2 p_to);
	PackedVector2Array find_path(Vector2 start, Vector2 end);

	_FORCE_INLINE_ void reset() {
		astar->clear();
		_destroy_drawer();
	}

	_FORCE_INLINE_ Rect2i get_region() const {
		return astar->get_region();
	}

	_FORCE_INLINE_ Vector2i get_size() const {
		return astar->get_size();
	}

	_FORCE_INLINE_ Vector2 get_cell_size() const {
		return astar->get_cell_size();
	}

	_FORCE_INLINE_ bool is_cell_solid(Vector2i cell) const {
		return astar->is_point_solid(cell);
	}

	_FORCE_INLINE_ Vector2 cell_to_world_gd(Vector2i cell) const {
		return _cell_to_world(cell);
	}

	_FORCE_INLINE_ void clear_drawer() {
		drawer = nullptr;
	}

private:
	Vector2i _world_to_cell(const Vector2 &pos) const;
	Vector2 _cell_to_world(const Vector2i &cell) const;
	Vector2 _cell_to_world_tl(const Vector2i &cell) const;
	void _set_point_solid(int cx, int cy, PackedVector2Array &world_poly);

	void _setup_astar(Node *root, Vector2i &grid_size, Vector2i &cell_size);

	void _process_rectangle_shape(Node2D *owner, CollisionShape2D *shape, bool add = true);
	void _process_static_obstacles(Node2D *body, bool add = true);
	void _process_tilemap_obstacles(TileMapLayer *tilemap, int p_layer_id = 0);
	void _process_sprite_obstacle(GdObj obj, bool add);

	Rect2 _get_scene_bounds(Node *root);
	void _destroy_drawer();
};

// 寻路网格的交互式调试覆盖层：绘制阻塞格、起终点与路径，并处理鼠标拖拽。
// 创建方为 SpxPathFinder::setup；直接回调方为 Godot 通知/输入系统；
// 顶层来源为 Godot 帧绘制和平台鼠标事件。Node 生命周期归场景树所有。
class PathDebugDrawer : public Node2D {
	GDCLASS(PathDebugDrawer, Node2D);

private:
	// 强引用寻路器，保证调试节点存活期间 A* 数据有效；退出树时解除反向裸指针。
	Ref<SpxPathFinder> path_finder;
	// 最近一次计算出的 Godot 世界坐标路径，用于 _draw。
	PackedVector2Array path;

	// 鼠标指定的路径起点（Godot 世界坐标）。
	Vector2 start;
	// 鼠标指定的路径终点（Godot 世界坐标）。
	Vector2 end;
	// 是否已设置起点。
	bool start_set = false;
	// 是否已设置终点。
	bool end_set = false;
	// 鼠标命中起终点、开始拖拽的世界距离阈值。
	float drag_threshold = 10.0;

	enum DragState { NONE,
		DRAG_START,
		DRAG_END };
	// 当前拖拽对象；只在 Godot 主线程输入回调内修改。
	DragState dragging = NONE;

protected:
	PathDebugDrawer() = default;
	~PathDebugDrawer() = default;

	// _bind_methods 由 ClassDB 注册阶段调用；_notification 由 Godot 引擎派发。
	static void _bind_methods();
	void _notification(int p_what);
	// 以下 ready/draw/exit_tree 由 _notification 转发，顶层是 SceneTree 生命周期/渲染。
	void _ready();
	void _draw();
	void _exit_tree();
	// Godot Viewport 输入传播回调；顶层来源为平台鼠标事件。
	void input(const Ref<InputEvent> &p_event) override;

public:
	explicit PathDebugDrawer(const Ref<SpxPathFinder> &p_path_finder) {
		path_finder = p_path_finder;
	}

	void set_path_finder(const Ref<SpxPathFinder> &p_path_finder);
	void set_path(const PackedVector2Array &p_path);

private:
	void _update_path();
	bool _is_near(const Vector2 &p1, const Vector2 &p2) const;
};

#endif // SPX_PATH_FINER_H
