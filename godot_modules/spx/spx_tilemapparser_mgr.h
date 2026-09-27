/**************************************************************************/
/*  spx_tilemapparser_mgr.h                                              */
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

#ifndef SPX_TILEMAP_PARSER_MGR_H
#define SPX_TILEMAP_PARSER_MGR_H

#include "gdextension_spx_ext.h"
#include "scene/resources/2d/tile_set.h"
#include "spx_manager.h"
#include "spx_tilemap_types.h"

class TileMapLayer;
class TileSetAtlasSource;
class TileData;

// 从 SPX JSON 在运行期重建 Godot TileSet/TileMapLayer 的 Manager，无需 Godot 导入流程。
// 数据链：JSON -> SpxTileMapData -> Godot Object；导出工具可执行反向序列化。
// 直接调用方：生成的 spx_tilemapparser_* ABI 包装和 SpxEngine 生命周期。
// 顶层调用方：Go Load/UnloadTilemap API、游戏 reset 或 Godot 退出。
// Godot 规则：Ref<TileSet> 由引用计数持有，add_source 后 TileSet 持有 atlas source；
// TileData* 只借用。TileMapLayer add_child 后归 SceneTree 所有，缓存中的裸指针不拥有
// 节点，卸载时应 queue_free 而不是立即 memdelete。
class SpxTilemapparserMgr : public SpxManager {

private:
	HashMap<String, Ref<TileSet>> tileset_cache; // 地图名到共享 TileSet 的强引用缓存。

	HashMap<String, Vector<TileMapLayer *>> tilemap_layers; // 地图名到已挂树层节点的借用指针索引。

private:
	// 解析后的数据到 Godot 对象的内部构建步骤，直接调用方均为 load_tilemap。
	Ref<TileSet> _create_tileset(const SpxTileSetData &data, const String &base_path);
	void _create_atlas_source(Ref<TileSet> tileset, const SpxTileSetSourceData &data, const String &base_path);
	void _setup_tile_physics(TileData *tile_data, const SpxTileData &data);
	TileMapLayer *_create_tilemap_layer(const SpxTileMapLayerData &data, Ref<TileSet> tileset, const Vector2 &node_offset);

	// 路径/Base64 辅助函数不持有资源。
	String _get_base_path(const String &json_path);
	Vector<uint8_t> _base64_decode(const String &base64_str);

public:
	// 生命周期直接由 SpxEngine 调用；顶层来自 reset 或 Godot 主循环 destroy。
	void on_destroy() override;
	void on_reset(int reset_code) override;

public:
	// SPX_BIND 直接由 ABI 调用；顶层来自 Go TilemapparserMgr。
	SPX_BIND void load_tilemap(GdString json_path);
	SPX_BIND void unload_tilemap(GdString name);
	SPX_BIND void destroy_all_tilemaps();

	// 查询接口。直接调用方：生成 ABI；顶层调用方：Go 侧地图存在性/层数查询。
	SPX_BIND GdBool has_tilemap(GdString name);
	SPX_BIND GdInt get_tilemap_layer_count(GdString name);
};

#endif // SPX_TILEMAP_PARSER_MGR_H
