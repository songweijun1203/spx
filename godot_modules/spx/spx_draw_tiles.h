/**************************************************************************/
/*  spx_draw_tiles.h                                                      */
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

#ifndef SPX_DRAW_TILES_H
#define SPX_DRAW_TILES_H

#include "core/templates/hash_map.h"
#include "scene/2d/camera_2d.h"
#include "scene/2d/node_2d.h"
#include "scene/2d/sprite_2d.h"
#include "scene/2d/tile_map_layer.h"
#include "scene/resources/2d/tile_set.h"
#include "spx_sprite.h"

// 瓦片编辑器一次 _draw() 使用的只读绘制快照。
// 由 SpxDrawTiles::_draw 构造并传给 LayerRenderer，不跨帧保存任何裸指针。
struct DrawContext {
	TileMapLayer *map_layer; // 当前编辑层的借用指针；挂树后由父节点/SceneTree 持有。

	Vector2i cell_size; // 单元格像素尺寸。
	Color grid_color; // 网格线颜色。
	float axis_width; // 坐标轴绘制宽度。
	int guide_rect_radius; // 鼠标指示框向外扩展的格数。

	Vector2 layer_pos; // 当前层相对编辑节点的位置。
	Vector2 mouse_pos; // 鼠标在编辑节点本地坐标中的位置。

	Rect2 used_rect; // 当前层已使用区域的像素包围盒。
	Ref<Texture2D> current_texture; // 当前画笔纹理，Ref 在本轮绘制期间保活。

	bool axis_flipped; // 坐标轴方向是否翻转。
	bool axis_dragging; // 当前是否正在拖动层原点。

	DrawContext &set_layer(TileMapLayer *layer) {
		map_layer = layer;
		return *this;
	}
	DrawContext &set_cell_size(Vector2i size) {
		cell_size = size;
		return *this;
	}
	DrawContext &set_grid_color(Color c) {
		grid_color = c;
		return *this;
	}
	DrawContext &set_axis_width(float w) {
		axis_width = w;
		return *this;
	}
	DrawContext &set_guide_rect_radius(int r) {
		guide_rect_radius = r;
		return *this;
	}

	DrawContext &set_layer_pos(Vector2 pos) {
		layer_pos = pos;
		return *this;
	}
	DrawContext &set_mouse_pos(Vector2 pos) {
		mouse_pos = pos;
		return *this;
	}

	DrawContext &set_current_texture(Ref<Texture2D> tex) {
		current_texture = tex;
		return *this;
	}
	DrawContext &set_flipped_axis(bool flipped) {
		axis_flipped = flipped;
		return *this;
	}
	DrawContext &set_axis_dragging(bool dragging) {
		axis_dragging = dragging;
		return *this;
	}
};

// 瓦片编辑辅助图形渲染器，只通过 Node2D::draw_* 写入父节点当前画布项。
// 直接调用方：SpxDrawTiles::_draw；顶层调用方：Godot NOTIFICATION_DRAW。
class LayerRenderer {
private:
	void _draw_axis(Node2D *parent_node, const DrawContext &ctx);
	void _draw_grid(Node2D *parent_node, const DrawContext &ctx, Vector2 hover_pos);
	void _draw_used_rect(Node2D *parent_node, const DrawContext &ctx);
	void _draw_guide_rect(Node2D *parent_node, const DrawContext &ctx, Vector2 hover_pos);
	void _draw_preview_texture(Node2D *parent_node, const DrawContext &ctx, Vector2 hover_pos);

public:
	LayerRenderer() = default;
	~LayerRenderer() = default;

	void draw(Node2D *parent_node, const DrawContext &ctx);
};

template <typename K, typename V>
// 小型双向映射，用于保持“路径<->纹理”和“纹理<->source_id”一致。
// 仅由 SpxDrawTiles 在 Godot 主线程访问，不承担并发同步。
class BiMap {
	HashMap<K, V> forward; // K 到 V 的正向索引。
	HashMap<V, K> backward; // V 到 K 的反向索引，必须与 forward 同步更新。

public:
	void insert(const K &k, const V &v) {
		forward[k] = v;
		backward[v] = k;
	}

	bool has_key(const K &k) const { return forward.has(k); }
	bool has_value(const V &v) const { return backward.has(v); }

	V get_value(const K &k) const { return forward[k]; }
	K get_key(const V &v) const { return backward[v]; }

	void erase_by_key(const K &k) {
		if (!forward.has(k)) {
			return;
		}
		auto v = forward[k];
		forward.erase(k);
		backward.erase(v);
	}

	void erase_by_value(const V &v) {
		if (!backward.has(v)) {
			return;
		}
		auto k = backward[v];
		backward.erase(v);
		forward.erase(k);
	}

	void clear() {
		forward.clear();
		backward.clear();
	}

	int size() const { return forward.size(); }
	bool empty() const { return forward.is_empty(); }
};

// 一次可撤销的格子变更，完整保存变更前/后的 TileSet 定位信息。
struct TileAction {
	int layer_index; // 目标 TileMapLayer 索引。
	Vector2i coords; // 层内网格坐标。
	bool placed; // true 表示原操作为放置，false 表示擦除。
	int source_id; // TileSet atlas source ID。
	Vector2i atlas_coord; // source 内的图集坐标。
	int alternative_tile; // Godot alternative tile 编号。
};

// SPX 运行时瓦片编辑节点：同时负责编辑器输入、预览绘制、TileSet 缓存与撤销栈。
// 直接调用方：SpxTilemapMgr；输入/绘制回调由 Godot SceneTree 调用。
// 顶层调用方：Go Tilemap API，或 Godot 输入传播与 CanvasItem 绘制阶段。
// Godot 规则：GDCLASS 节点的方法需在 _bind_methods 注册后才能被反射；queue_redraw()
// 只请求未来的 NOTIFICATION_DRAW，不能期待调用点立即执行 _draw()。TileSet 与 atlas
// source 都是 RefCounted 资源；get_tile_data() 返回的 TileData* 只借用，不能自行释放。
class SpxDrawTiles : public Node2D {
	GDCLASS(SpxDrawTiles, Node2D);

private:
	Ref<TileSet> shared_tile_set; // 所有动态层共用的强引用资源；层和本节点共同为其保活。
	Ref<Texture2D> current_texture; // 当前编辑笔刷的原始纹理。

	Vector<TileAction> undo_stack; // 已执行操作，末尾为下一次撤销目标。
	Vector<TileAction> redo_stack; // 已撤销操作，执行新操作时清空。

	BiMap<String, Ref<Texture2D>> path_cached_textures_bimap; // SPX 路径与已加载原纹理的双向缓存。
	BiMap<Ref<Texture2D>, int> scaled_texture_source_ids_bimap; // 缩放纹理与 TileSet source ID 的双向缓存。
	HashMap<Ref<Texture2D>, Ref<ImageTexture>> texture_scaled_cache_map; // 原纹理到单格尺寸纹理的缓存。
	HashMap<Ref<ImageTexture>, String> scaled_texture_path_map; // 缩放纹理回查原 SPX 路径。
	HashMap<int, TileMapLayer *> index_layer_map; // 层号到 SceneTree 子节点的借用指针映射。
	int max_layer_index = -1; // 当前创建过的最大层号，用于保持层级顺序。
	int next_source_id = 1; // 下一个动态 TileSetAtlasSource ID；0 保留给 Godot/既有数据。

	const String UNIQUE_LAYER_PREFIX = "spx_draw_tiles_layer_"; // 动态层节点名的稳定前缀。
	Vector2i default_cell_size{ 16, 16 }; // 默认方形格尺寸。
	Vector2i default_atlas_coord{ 0, 0 }; // 每个独立纹理 source 使用的默认 atlas 坐标。
	int default_physics_layer = 0; // 自动碰撞多边形写入的 TileSet 物理层。
	int current_layer_index = 0; // 当前编辑目标层号。

	bool exit_editor = true; // true 时关闭交互辅助绘制，但保留已有瓦片。
	bool axis_flipped = false; // 编辑坐标轴是否翻转。
	bool axis_dragging = false; // 鼠标是否已进入层原点拖动状态。
	bool tile_placing = false; // 本次按压是否正在连续放置/擦除瓦片。
	static constexpr float drag_threshold = 10.0; // 从点击切换为拖动的像素阈值。
	Vector2 drag_start; // 本次拖动开始时的鼠标位置。
	Vector2 layer_start_pos; // 本次拖动开始时当前层的位置。

	const Color GRID_COLOR{ 1.0, 1.0, 0.0, 0.5 }; // 编辑网格颜色。
	static constexpr int GUIDE_RECT_RADIUS = 5; // 指示区域半径（格）。
	static constexpr float AXIS_WIDTH = 5; // 原点坐标轴宽度（像素）。
	LayerRenderer renderer; // 无所有权资源的即时辅助绘制器。

protected:
	static void _bind_methods();
	void _notification(int p_what);
	void _ready();
	void _process(double delta);
	void _draw();
	void input(const Ref<InputEvent> &p_event) override;

public:
	SpxDrawTiles() = default;
	~SpxDrawTiles() = default;

	// 复用的默认碰撞点集；静态存储只保存数值，不持有 Godot Object。
	static inline Vector<Vector2> default_collision_rect{}; // 随格尺寸刷新的一格矩形碰撞多边形。
	static inline Vector<Vector2> no_collision_array{}; // 显式表示当前瓦片不创建碰撞多边形。

	// 根据当前格尺寸刷新默认矩形碰撞点集。
	void update_default_collision_rect();

	// 直接调用方：SpxTilemapMgr；顶层调用方：Go engine.TilemapMgr 的对应接口。
	void set_layer_index_spx(GdInt index);
	void set_tile_texture_spx(GdString texture_path, const Vector<Vector2> *collision_points);
	void place_tiles_spx(GdArray positions, GdString texture_path);
	void place_tiles_spx(GdArray positions, GdString texture_path, GdInt layer_index);
	void place_tile_spx(GdVec2 pos, GdString texture_path);
	void place_tile_spx(GdVec2 pos, GdString texture_path, GdInt layer_index);
	void erase_tile_spx(GdVec2 pos, GdInt layer_index);
	void erase_tile_spx(GdVec2 pos);
	GdString get_tile_spx(GdVec2 pos, GdInt layer_index);
	GdString get_tile_spx(GdVec2 pos);

	void set_tile_size(int size = 16);
	void set_layer_index(int index);
	void set_layer_offset_spx(int index, Vector2 offset);
	Vector2 get_layer_offset_spx(int index);
	void set_texture(Ref<Texture2D> texture, bool with_collision = true);
	void set_texture_with_collision_points(Ref<Texture2D> texture, const Vector<Vector2> *collision_points);

	void place_tile(TileMapLayer *layer, Vector2i coords);
	void erase_tile(TileMapLayer *layer, Vector2i coords);
	void place_or_erase_tile(Vector2 pos, bool erase);

	void undo();
	void redo();

	void clear_all_layers();

	_FORCE_INLINE_ void enter_editor_mode() { exit_editor = false; }
	_FORCE_INLINE_ void exit_editor_mode() {
		exit_editor = true;
		queue_redraw();
	}

private:
	TileMapLayer *_get_or_create_layer(int layer_index);
	TileMapLayer *_get_layer(int layer_index);
	TileMapLayer *_create_layer(int layer_index);

	int _get_or_create_source_id(Ref<Texture2D> scaled_texture, bool with_collision = true);
	int _get_or_create_source_id_with_collision(Ref<Texture2D> scaled_texture, const Vector<Vector2> *collision_points);

	bool _create_tile(Ref<TileSetAtlasSource> atlas_source, const Vector2i &tile_coords, const Vector<Vector2> *collision_points);
	Ref<ImageTexture> _get_or_create_scaled_texture(Ref<Texture2D> texture);
	String _get_tile_texture_path(TileMapLayer *layer, const Vector2i &pos);

	void _place_tiles_bulk_spx(GdArray positions);
	void _place_tile_spx(GdVec2 pos);

	void _destroy_layers();
	void _clear_cache();

	bool _apply_action(const TileAction &action, bool inverse);
};

#endif // SPX_DRAW_TILES_H
