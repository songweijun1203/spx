/**************************************************************************/
/*  spx_tilemap_types.h                                                   */
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

#ifndef SPX_TILEMAP_TYPES_H
#define SPX_TILEMAP_TYPES_H

#include "core/math/vector2.h"
#include "core/math/vector2i.h"
#include "core/string/ustring.h"
#include "core/templates/vector.h"
#include "core/variant/array.h"
#include "core/variant/dictionary.h"

// 一个 Godot TileSet 物理层的序列化配置。
struct SpxPhysicsLayerData {
	uint32_t collision_layer = 1; // 该层产生的碰撞位。
	uint32_t collision_mask = 1; // 该层参与查询的目标位。

	bool from_json(const Dictionary &dict);
	Dictionary to_json() const;
};

// 单个瓦片在某个 TileSet 物理层上的碰撞多边形集合。
struct SpxTilePhysicsData {
	int layer = 0; // TileSet 物理层索引。
	Vector<Vector<float>> polygons; // 每个多边形为扁平数组 [x1,y1,x2,y2,...]。

	bool from_json(const Dictionary &dict);
	Dictionary to_json() const;
};

// 一个 atlas 瓦片的序列化数据。
struct SpxTileData {
	Vector2i atlas_coords; // 瓦片左上角在 atlas 网格中的坐标。
	Vector2i size_in_atlas = Vector2i(1, 1); // 瓦片跨越的 atlas 单元数。
	Vector<SpxTilePhysicsData> physics; // 各物理层的碰撞多边形。

	bool from_json(const Dictionary &dict);
	Dictionary to_json() const;
};

// 一个 Godot TileSetAtlasSource 的序列化数据。
struct SpxTileSetSourceData {
	int id = 0; // 在 TileSet 内唯一的 source ID。
	String type = "atlas"; // source 类型；当前仅支持 atlas。
	String texture; // 相对 JSON 文件的纹理路径。
	Vector2i texture_region_size; // atlas 单格像素尺寸。
	Vector2i margins; // 纹理外边距。
	Vector2i separation; // atlas 单元之间的像素间隔。
	Vector<SpxTileData> tiles; // source 中显式存在的瓦片。

	bool from_json(const Dictionary &dict);
	Dictionary to_json() const;
};

// 完整 Godot TileSet 的序列化数据。
struct SpxTileSetData {
	Vector2i tile_size = Vector2i(16, 16); // 地图逻辑单格尺寸。
	String tile_shape = "square"; // Godot TileSet 形状名称。
	Vector<SpxPhysicsLayerData> physics_layers; // 物理层配置。
	Vector<SpxTileSetSourceData> sources; // atlas source 列表。

	bool from_json(const Dictionary &dict);
	Dictionary to_json() const;
};

// 一个 Godot TileMapLayer 节点的序列化数据。
struct SpxTileMapLayerData {
	String name; // 层节点名。
	int z_index = 0; // CanvasItem 绘制层级。
	Vector2 offset; // 层相对地图根节点的像素偏移。
	bool enabled = true; // 层是否启用。
	String tile_map_data_base64; // Godot TileMap 二进制单元数据的 Base64 表示。

	bool from_json(const Dictionary &dict);
	Dictionary to_json() const;
};

// JSON 文件的根结构，描述一个可在运行期重建的完整 TileMap。
// 直接调用方：SpxTilemapparserMgr::load_tilemap/save 工具。
// 顶层调用方：Go LoadTilemap API 或项目导出工具。
struct SpxTileMapData {
	int version = 1; // JSON schema 版本。
	String name; // 地图逻辑名，也是运行期缓存键。
	Vector2 node_offset; // 将地图中心对齐 SPX 原点使用的像素偏移。
	SpxTileSetData tileset; // 共享 TileSet 描述。
	Vector<SpxTileMapLayerData> layers; // 按场景顺序排列的地图层。

	bool from_json(const Dictionary &dict);
	Dictionary to_json() const;

	// 解析入口直接由 SpxTilemapparserMgr 调用；顶层来自 Go LoadTilemap。
	static bool parse_from_file(const String &json_path, SpxTileMapData &out_data);
	// 导出辅助入口，写入必须遵守 Godot FileAccess 的 res://、user:// 权限规则。
	bool save_to_file(const String &json_path) const;
};

#endif // SPX_TILEMAP_TYPES_H
