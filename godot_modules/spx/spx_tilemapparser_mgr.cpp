/**************************************************************************/
/*  spx_tilemapparser_mgr.cpp                                            */
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

#include "spx_tilemapparser_mgr.h"

#include "core/core_bind.h"
#include "core/io/marshalls.h"
#include "scene/2d/tile_map_layer.h"
#include "scene/resources/2d/tile_set.h"
#include "spx_engine.h"
#include "spx_res_mgr.h"

// ============================================================================
// 生命周期入口。直接调用方：SpxEngine；顶层调用方：Go runtime 的重置/销毁流程。
// ============================================================================

void SpxTilemapparserMgr::on_destroy() {
	destroy_all_tilemaps();
	tileset_cache.clear();
}

void SpxTilemapparserMgr::on_reset(int reset_code) {
	destroy_all_tilemaps();
	tileset_cache.clear();
}

// ============================================================================
// 对外接口。直接调用方：生成的 SPX ABI；顶层调用方：Go TilemapParser API。
// ============================================================================

void SpxTilemapparserMgr::load_tilemap(GdString json_path) {
	String path = SpxStr(json_path);
	String engine_path = resMgr->_to_engine_path(path);

	// 第一阶段：JSON -> 与引擎对象解耦的数据结构，由 SpxTileMapData 负责。
	SpxTileMapData data;
	if (!SpxTileMapData::parse_from_file(engine_path, data)) {
		print_error("SpxTilemapparserMgr: Failed to parse tilemap JSON: " + engine_path);
		return;
	}

	// 地图名优先级：JSON name、父目录名、文件基础名；该名字也是缓存键。
	String tilemap_name = data.name;
	if (tilemap_name.is_empty()) {
		// 例如 "tilemaps/map2/tilemap.json" 推导为 "map2"。
		String dir_path = path.get_base_dir();
		tilemap_name = dir_path.get_file();
		if (tilemap_name.is_empty()) {
			// 没有父目录时退回文件基础名。
			tilemap_name = path.get_file().get_basename();
		}
	}

	if (tilemap_layers.has(tilemap_name)) {
		print_line("SpxTilemapparserMgr: Tilemap already loaded: " + tilemap_name);
		return;
	}

	// 纹理路径相对 JSON 所在目录解析。
	String base_path = _get_base_path(engine_path);

	// 第二阶段：数据结构 -> Godot TileSet/TileMapLayer，由本 Manager 负责。
	Ref<TileSet> tileset = _create_tileset(data.tileset, base_path);
	if (tileset.is_null()) {
		print_error("SpxTilemapparserMgr: Failed to create TileSet for: " + tilemap_name);
		return;
	}

	tileset_cache[tilemap_name] = tileset;

	Vector<TileMapLayer *> layers;
	for (int i = 0; i < data.layers.size(); i++) {
		TileMapLayer *layer = _create_tilemap_layer(data.layers[i], tileset, data.node_offset);
		if (layer != nullptr) {
			Node *spx_root = get_spx_root();
			if (spx_root != nullptr) {
				// Godot 规则：只有挂入 SceneTree 的 Node 才参与渲染、物理与生命周期通知。
				spx_root->add_child(layer);
			}
			layers.push_back(layer);
		}
	}

	tilemap_layers[tilemap_name] = layers;
}

void SpxTilemapparserMgr::unload_tilemap(GdString name) {
	String tilemap_name = SpxStr(name);

	if (tilemap_layers.has(tilemap_name)) {
		Vector<TileMapLayer *> &layers = tilemap_layers[tilemap_name];
		for (TileMapLayer *layer : layers) {
			if (layer != nullptr) {
				// Godot 规则：遍历场景树期间不能立即 delete Node，使用 queue_free 延迟释放。
				layer->queue_free();
			}
		}
		tilemap_layers.erase(tilemap_name);
	}

	tileset_cache.erase(tilemap_name);
}

void SpxTilemapparserMgr::destroy_all_tilemaps() {
	for (KeyValue<String, Vector<TileMapLayer *>> &kv : tilemap_layers) {
		for (TileMapLayer *layer : kv.value) {
			if (layer != nullptr) {
				layer->queue_free();
			}
		}
	}
	tilemap_layers.clear();
	tileset_cache.clear();
}

// ============================================================================
// 查询接口。直接调用方：SPX ABI；顶层调用方：Go 侧地图存在性与层数查询。
// ============================================================================

GdBool SpxTilemapparserMgr::has_tilemap(GdString name) {
	String tilemap_name = SpxStr(name);
	return tilemap_layers.has(tilemap_name);
}

GdInt SpxTilemapparserMgr::get_tilemap_layer_count(GdString name) {
	String tilemap_name = SpxStr(name);
	if (tilemap_layers.has(tilemap_name)) {
		return tilemap_layers[tilemap_name].size();
	}
	return 0;
}

// ============================================================================
// 纯辅助方法，只由本类加载流程直接调用。
// ============================================================================

String SpxTilemapparserMgr::_get_base_path(const String &json_path) {
	int last_slash = json_path.rfind("/");
	if (last_slash == -1) {
		last_slash = json_path.rfind("\\");
	}

	if (last_slash != -1) {
		return json_path.substr(0, last_slash);
	}
	return "";
}

Vector<uint8_t> SpxTilemapparserMgr::_base64_decode(const String &base64_str) {
	PackedByteArray decoded = core_bind::Marshalls::get_singleton()->base64_to_raw(base64_str);

	Vector<uint8_t> result;
	result.resize(decoded.size());
	for (int i = 0; i < decoded.size(); i++) {
		result.write[i] = decoded[i];
	}

	return result;
}

// ============================================================================
// Godot 对象构建阶段，只由 load_tilemap 的第二阶段直接调用。
// ============================================================================

Ref<TileSet> SpxTilemapparserMgr::_create_tileset(const SpxTileSetData &data, const String &base_path) {
	Ref<TileSet> tileset;
	tileset.instantiate();

	tileset->set_tile_size(data.tile_size);

	for (int i = 0; i < data.physics_layers.size(); i++) {
		const SpxPhysicsLayerData &layer_data = data.physics_layers[i];
		tileset->add_physics_layer();
		tileset->set_physics_layer_collision_layer(i, layer_data.collision_layer);
		tileset->set_physics_layer_collision_mask(i, layer_data.collision_mask);
	}

	for (int i = 0; i < data.sources.size(); i++) {
		_create_atlas_source(tileset, data.sources[i], base_path);
	}

	return tileset;
}

void SpxTilemapparserMgr::_create_atlas_source(Ref<TileSet> tileset, const SpxTileSetSourceData &data, const String &base_path) {
	// 当前 SPX 文件协议只支持 atlas 类型 source。
	if (data.type != "atlas") {
		print_line("SpxTilemapparserMgr: Skipping non-atlas source type: " + data.type);
		return;
	}

	// 经 SPX 资源管理器加载，以兼容虚拟资源路径和平台差异。
	if (data.texture.is_empty()) {
		print_error("SpxTilemapparserMgr: Source " + itos(data.id) + " has no texture");
		return;
	}

	// 将 JSON 中的相对纹理路径解析为引擎路径。
	String full_texture_path = base_path.is_empty() ? data.texture : base_path.path_join(data.texture);
	Ref<Texture2D> texture = resMgr->load_texture(full_texture_path, true);
	if (texture.is_null()) {
		print_error("SpxTilemapparserMgr: Failed to load texture: " + full_texture_path);
		return;
	}

	Ref<TileSetAtlasSource> atlas;
	atlas.instantiate();
	atlas->set_texture(texture);
	atlas->set_texture_region_size(data.texture_region_size);
	atlas->set_margins(data.margins);
	atlas->set_separation(data.separation);

	// Godot 规则：必须先 add_source，使 atlas 获得所属 TileSet，并按 TileSet 的物理层
	// 初始化 TileData 内部数组；若先 create_tile，随后写碰撞层会访问到未初始化状态。
	tileset->add_source(atlas, data.id);

	// 此时创建的 TileData 已拥有正确的物理层数量；返回的 TileData* 归 atlas 所有，
	// 这里只在 source 存活期间临时借用，不能自行释放。
	for (int i = 0; i < data.tiles.size(); i++) {
		const SpxTileData &tile_data = data.tiles[i];

		atlas->create_tile(tile_data.atlas_coords, tile_data.size_in_atlas);

		TileData *td = atlas->get_tile_data(tile_data.atlas_coords, 0);
		if (td != nullptr) {
			_setup_tile_physics(td, tile_data);
		}
	}
}

void SpxTilemapparserMgr::_setup_tile_physics(TileData *tile_data, const SpxTileData &data) {
	for (int i = 0; i < data.physics.size(); i++) {
		const SpxTilePhysicsData &phys_data = data.physics[i];
		int layer_id = phys_data.layer;

		for (int p = 0; p < phys_data.polygons.size(); p++) {
			const Vector<float> &polygon_flat = phys_data.polygons[p];

			// 文件协议用 [x0,y0,x1,y1,...]；Godot 碰撞接口需要 Vector<Vector2>。
			Vector<Vector2> polygon;
			int point_count = polygon_flat.size() / 2;
			polygon.resize(point_count);
			for (int k = 0; k < point_count; k++) {
				polygon.write[k] = Vector2(polygon_flat[k * 2], polygon_flat[k * 2 + 1]);
			}

			if (polygon.size() >= 3) {
				// Godot 要求先创建对应序号的碰撞多边形，才能设置其点集。
				while (tile_data->get_collision_polygons_count(layer_id) <= p) {
					tile_data->add_collision_polygon(layer_id);
				}
				tile_data->set_collision_polygon_points(layer_id, p, polygon);
			}
		}
	}
}

TileMapLayer *SpxTilemapparserMgr::_create_tilemap_layer(const SpxTileMapLayerData &data, Ref<TileSet> tileset, const Vector2 &node_offset) {
	// 返回尚未挂树的新 Node；调用方必须通过 add_child 将所有权转交父节点/SceneTree。
	TileMapLayer *layer = memnew(TileMapLayer);

	layer->set_name(data.name.is_empty() ? "layer" : data.name);
	layer->set_z_index(data.z_index);

	// 将图层偏移与地图整体偏移合并，保持导出时的居中位置。
	layer->set_position(data.offset + node_offset);

	layer->set_enabled(data.enabled);
	layer->set_tile_set(tileset);

	// tile_map_data 是 Godot TileMapLayer 原生二进制数据的 Base64 表示。
	if (!data.tile_map_data_base64.is_empty()) {
		Vector<uint8_t> bytes = _base64_decode(data.tile_map_data_base64);
		if (bytes.size() > 0) {
			layer->set_tile_map_data_from_array(bytes);
		}
	}

	return layer;
}
