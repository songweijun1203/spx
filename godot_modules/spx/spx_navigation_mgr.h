/**************************************************************************/
/*  spx_navigation_mgr.h                                                  */
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

#ifndef SPX_NAVIGATION_MGR_H
#define SPX_NAVIGATION_MGR_H

#include "gdextension_spx_ext.h"
#include "spx_manager.h"
#include "spx_path_finder.h"

// SPX 网格寻路门面，按需创建 SpxPathFinder，并把场景静态碰撞体、TileMapLayer
// 和静态精灵转换为 AStarGrid2D 障碍。
// 直接调用方：生成的 spx_navigation_* ABI 和 SpxEngine reset 分发；
// 顶层调用方：Go NavigationMgr/精灵寻路 API。扫描场景树和调试 Node 均须在主线程。
class SpxNavigationMgr : public SpxManager {
public:
	// 直接调用方：SpxEngine::_notify_managers；顶层来源：Go 游戏 reset/重载。
	void on_reset(int reset_code) override;

private:
	// 引用计数拥有寻路器；reset 释放引用，调试节点由寻路器安排 queue_free。
	Ref<SpxPathFinder> path_finder;
	// 未显式配置时采用的最大网格尺寸（格数）。
	const GdVec2 default_grid_size{ 100, 100 };
	// 未显式配置时采用的单元格世界尺寸（Godot 像素）。
	const GdVec2 default_cell_size{ 16, 16 };

public:
	// 以下 SPX_BIND 由 ABI 直接调用，顶层为 Go NavigationMgr/寻路功能。
	SPX_BIND void setup_path_finder_with_size(GdVec2 grid_size, GdVec2 cell_size, GdBool with_jump, GdBool with_debug);
	SPX_BIND void setup_path_finder(GdBool with_jump);
	SPX_BIND void set_obstacle(GdObj obj, GdBool enabled);
	SPX_BIND GdArray find_path(GdVec2 p_from, GdVec2 p_to, GdBool with_jump);
};

#endif // SPX_NAVIGATION_MGR_H
