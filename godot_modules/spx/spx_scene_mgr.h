/**************************************************************************/
/*  spx_scene_mgr.h                                                       */
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

#ifndef SPX_SCENE_MGR_H
#define SPX_SCENE_MGR_H

#include "gdextension_spx_ext.h"
#include "core/templates/rb_map.h"
#include "spx_manager.h"

class ISortableSprite;
class TileMapLayer;
class SubViewport;

// 场景级能力管理器：切换/重载场景，管理不带完整 SPX 行为的轻量精灵，并将当前
// 瓦片场景离屏渲染为 PNG。
// 直接调用方：原生/Web 生成 ABI、SpxSpriteMgr、SpxPathFinder 和 SpxDrawTiles；
// 顶层调用方：Go Scene API、图层排序/寻路刷新以及瓦片编辑导出命令。
// Godot 规则：Node 必须在主线程加入 SceneTree；销毁用 queue_free；SubViewport 纹理
// 需等待至少一次渲染更新后读取，不能在创建当帧立即保存。
class SpxSceneMgr : public SpxManager {

private:
	const String DEFAULT_SAVE_PATH = "user://exported_scene.png"; // Godot 可写用户目录中的默认导出路径。
	bool export_pending = false; // 是否有 SubViewport 等待渲染完成后读取。
	double elapsed = 0.0; // 从导出请求开始累计的主循环时间。
	// 当前待导出视口的借用指针；父场景持有节点，仅在 export_pending 为 true 时使用，
	// 保存完成后由本类请求 queue_free。
	SubViewport *viewport_to_export = nullptr;
	Vector2 cached_cell_size{ 16, 16 }; // 计算 TileMapLayer 边界使用的格尺寸。

	void _request_export(SubViewport *viewport);
	void _export_vp_png(SubViewport *viewport);

public:
	// 轻量精灵不创建完整 SpxSprite 行为/同步状态，只参与渲染、可选静态碰撞和排序。
	// 挂树后由父节点/SceneTree 持有；本类借用指针并负责在 reset/destroy 时请求 queue_free。
	Node *pure_sprite_root = nullptr;
	RBMap<GdObj, ISortableSprite *> id_pure_sprites; // Go 对象 ID 到场景节点的借用指针索引。

	void on_awake() override;
	void on_update(float delta) override;
	void on_destroy() override;
	void on_reset(int reset_code) override;

	void collect_sortable_sprites(Vector<ISortableSprite *> &out);

	_FORCE_INLINE_ void set_cached_cell_size(Vector2 cell_size) {
		cached_cell_size = cell_size;
	}
	Rect2 get_scene_bounds(Node *node);
	Rect2 get_tilemap_bounds(TileMapLayer *layer);

	void export_scene_as_png(Node *root);

public:

	SPX_BIND void change_scene_to_file(GdString path);
	SPX_BIND void destroy_all_sprites();
	SPX_BIND GdInt reload_current_scene();
	SPX_BIND void unload_current_scene();

	// 轻量精灵接口。直接调用方：生成 ABI；顶层调用方：Go Scene/CreateSprite API。
	SPX_BIND void clear_pure_sprites();
	SPX_BIND void create_pure_sprite(GdString texture_path, GdVec2 pos, GdInt zindex);
	SPX_BIND void destroy_pure_sprite(GdObj id);

	SPX_BIND GdObj create_render_sprite(GdString texture_path, GdVec2 pos, GdFloat degree, GdVec2 scale, GdInt zindex, GdVec2 pivot);
	SPX_BIND GdObj create_static_sprite(GdString texture_path, GdVec2 pos, GdFloat degree, GdVec2 scale, GdInt zindex, GdVec2 pivot, GdInt collider_type, GdVec2 collider_pivot, GdArray collider_params);
};

#endif // SPX_SCENE_MGR_H
