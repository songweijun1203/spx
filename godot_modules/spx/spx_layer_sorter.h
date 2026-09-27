/**************************************************************************/
/*  spx_layer_sorter.h                                                    */
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

#ifndef SPX_LAYER_SORTER_H
#define SPX_LAYER_SORTER_H

#include "core/object/object.h"
#include "scene/2d/node_2d.h"
#include "spx_sprite.h"
#include <algorithm>
#include <functional>
#include <unordered_set>
#include <vector>

class ISortableSprite;
class LayerSorterDebugDrawer;
class Font;

// 一次分层排序所需的非拥有快照；sortable 的真实生命周期由精灵/场景树管理。
struct SortInfo {
	// SPX 对象稳定 ID，用于去重和跨帧匹配。
	GdObj id;
	// 上次采样的 Godot 世界坐标排序点。
	Point2 pos;
	// 可排序精灵接口的非拥有裸指针；使用前必须调用 is_node_valid。
	ISortableSprite *sortable;
};

enum class LayerSortMode {
	NONE,
	VERTICAL
};

// SPX 精灵纵向分层单例。合并静态/动态精灵排序，增量更新 z_index，并依据相机矩形
// 通知可见性变化。直接调用方：SpxSpriteMgr::on_update、SpxExtMgr、SpxSceneMgr；
// 顶层调用方：Godot 每帧主循环、Go 项目 layerSortMode 配置和场景重载。
// 容器持有非拥有裸指针，必须在场景销毁前 reset；CanvasItem z_index、调试 Node
// 和相机均只能在 Godot 主线程访问。
class SpxLayerSorter {
public:
	// 可见性变化回调；由 SpriteMgr 安装，参数指针只在回调期间有效。
	using VisibilityCallback = std::function<void(ISortableSprite *, bool visible)>;

	static SpxLayerSorter &instance() {
		static SpxLayerSorter inst;
		return inst;
	}

	void set_mode(LayerSortMode mode);
	void update(const Vector<ISortableSprite *> &sortables);

	void add_static_sprite(ISortableSprite *sp);
	void remove_static_sprite(ISortableSprite *sp);

	_FORCE_INLINE_ void reset() {
		static_sorted.clear();
		dynamic_sorted.clear();
		dynamic_dirty.clear();
		dynamic_dirty_ids.clear();
		visible_ids.clear();
		_clear_drawer();
	}
	_FORCE_INLINE_ void set_screen_rect(const Rect2 &rect) {
		screen_rect = rect;
	}
	_FORCE_INLINE_ void set_visibility_callback(VisibilityCallback cb) {
		visibility_callback = std::move(cb);
	}

private:
	// 静态精灵有序表；位置通常不变，仅新增/删除或首次构建时重排。
	std::vector<SortInfo> static_sorted;
	// 动态精灵有序表；每帧仅重插入 dirty 项，变化过多时退化为全量排序。
	std::vector<SortInfo> dynamic_sorted;
	// 本帧位置变化的动态精灵快照。
	std::vector<SortInfo> dynamic_dirty;
	// dirty ID 去重集合，防止同一精灵一帧重复入队。
	std::unordered_set<GdObj> dynamic_dirty_ids;

	// 当前相机可见世界矩形，用于裁剪动态排序和计算可见性事件。
	Rect2 screen_rect;
	// 上帧可见的对象 ID 集合，用于只发状态变化回调。
	std::unordered_set<GdObj> visible_ids;
	// SpriteMgr 注入的可见性回调；不拥有其捕获对象，reset/销毁顺序需由引擎保证。
	VisibilityCallback visibility_callback;

	// 场景树拥有的调试绘制节点；退出树时会调用 unlink_drawer。
	LayerSorterDebugDrawer *drawer = nullptr;

	// 全局排序模式，由 Go 项目配置经 SpxExtMgr 设置。
	static inline LayerSortMode sort_mode = LayerSortMode::NONE;

	// dirty 占动态表比例超过该阈值时执行全量排序。
	static inline float full_sort_ratio = 0.3f;
	// 静态表自最近一次结构变化后是否已经排序。
	static inline bool static_initialized = false;
	static inline bool sprite_cmp(const SortInfo &a, const SortInfo &b) {
		if (a.pos.y == b.pos.y) {
			return a.pos.x > b.pos.x;
		}
		return a.pos.y < b.pos.y;
	}

	void _mark_dirty(ISortableSprite *sp);
	void _update_visibility(const Vector<ISortableSprite *> &sortables);
	void _collect_sprites(const Vector<ISortableSprite *> &sortables);
	void _incremental_sort_dynamic();
	void _full_sort_dynamic();
	void _apply_z_index_merged();

	void _create_drawer();
	void _clear_drawer();

private:
	SpxLayerSorter() {
		reset();
	}
	~SpxLayerSorter();

	SpxLayerSorter(const SpxLayerSorter &) = delete;
	SpxLayerSorter &operator=(const SpxLayerSorter &) = delete;

public:
	_FORCE_INLINE_ const Rect2 &get_screen_rect() const {
		return screen_rect;
	}
	_FORCE_INLINE_ const std::vector<SortInfo> &get_static_sorted() const {
		return static_sorted;
	}
	_FORCE_INLINE_ const std::vector<SortInfo> &get_dynamic_sorted() const {
		return dynamic_sorted;
	}
	_FORCE_INLINE_ void unlink_drawer() {
		drawer = nullptr;
	}
};

// 分层排序调试覆盖层，绘制静态/动态排序点、序号和相机矩形。
// 创建方为 SpxLayerSorter；直接回调方是 Godot 通知/CanvasItem 绘制系统；
// 顶层来源为 Godot 帧渲染。Node 所有权属于场景树。
class LayerSorterDebugDrawer : public Node2D {
	GDCLASS(LayerSorterDebugDrawer, Node2D);

private:
	// 非拥有排序器指针；排序器是进程期单例，退出树时解除其 drawer 反向指针。
	SpxLayerSorter *sorter = nullptr;
	// 引用计数拥有的主题默认字体，用于绘制排序序号。
	Ref<Font> font;

protected:
	LayerSorterDebugDrawer() = default;
	~LayerSorterDebugDrawer() = default;

	// _notification 由 Godot 引擎派发；ready/draw/exit_tree 仅由其转发调用。
	static void _bind_methods();
	void _notification(int p_what);
	void _ready();
	void _draw();
	void _exit_tree();

public:
	explicit LayerSorterDebugDrawer(SpxLayerSorter *p_sorter) {
		sorter = p_sorter;
	}

	_FORCE_INLINE_ void update() {
		queue_redraw();
	}
};

#endif //SPX_LAYER_SORTER_H
