/**************************************************************************/
/*  spx_tilemap_mgr.h                                                     */
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

#ifndef SPX_TILEMAP_MGR_H
#define SPX_TILEMAP_MGR_H

#include "gdextension_spx_ext.h"
#include "spx_manager.h"

class SpxDrawTiles;

// Go Tilemap Manager 在 Godot 侧的轻量门面，按需创建当前受控的 SpxDrawTiles 节点。
// 直接调用方：生成的 spx_tilemap_* Native/Web ABI 包装和 SpxEngine 生命周期。
// 顶层调用方：Go Tilemap API、游戏 reset 或 Godot 进程退出。
// Godot 规则：draw_tiles 挂入 SceneTree 后由父节点持有；这里只保存借用指针。
class SpxTilemapMgr : public SpxManager {
public:
	void on_destroy() override;
	void on_reset(int reset_code) override;

private:
	// 当前受控编辑节点的借用指针；关闭/reset/destroy 会请求 queue_free，而退出编辑模式
	// 会保留已绘制节点并仅放弃该指针，后续重新打开可创建新的受控节点。
	SpxDrawTiles *draw_tiles = nullptr;
	SpxDrawTiles *_ensure_draw_tiles();

public:
	// SPX_BIND 方法直接由 ABI 调用；顶层均来自 Go engine.TilemapMgr。
	SPX_BIND void open_draw_tiles_with_size(GdInt tile_size);
	SPX_BIND void open_draw_tiles();
	SPX_BIND void set_layer_index(GdInt index);
	SPX_BIND void set_tile(GdString texture_path, GdBool with_collision);
	SPX_BIND void set_tile_with_collision_info(GdString texture_path, GdArray collision_points);
	SPX_BIND void set_layer_offset(GdInt index, GdVec2 offset);
	SPX_BIND GdVec2 get_layer_offset(GdInt index);
	SPX_BIND void place_tiles(GdArray positions, GdString texture_path);
	SPX_BIND void place_tiles_with_layer(GdArray positions, GdString texture_path, GdInt layer_index);
	SPX_BIND void place_tile(GdVec2 pos, GdString texture_path);
	SPX_BIND void place_tile_with_layer(GdVec2 pos, GdString texture_path, GdInt layer_index);
	SPX_BIND void erase_tile(GdVec2 pos);
	SPX_BIND void erase_tile_with_layer(GdVec2 pos, GdInt layer_index);
	SPX_BIND GdString get_tile(GdVec2 pos);
	SPX_BIND GdString get_tile_with_layer(GdVec2 pos, GdInt layer_index);
	SPX_BIND void close_draw_tiles();
	SPX_BIND void exit_tilemap_editor_mode();

};

#endif // SPX_TILEMAP_MGR_H
