/**************************************************************************/
/*  spx_manager.cpp                                                      */
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

#include "spx_manager.h"

#include "spx_engine.h"

GdInt SpxManager::get_unique_id() {
	// 直接调用方：各派生 Manager 创建 Sprite/UI/资源对象；顶层调用方：Go 对象创建 API。
	return SpxEngine::get_singleton()->get_unique_id();
}

Window *SpxManager::get_root() {
	// 直接调用方：派生 Manager；顶层调用方：需要访问 Godot 根窗口的 Go API。
	return SpxEngine::get_singleton()->get_root();
}

Node *SpxManager::get_spx_root() {
	// 直接调用方：派生 Manager 创建场景节点；顶层调用方：Go 侧对象/绘制 API。
	return SpxEngine::get_singleton()->get_spx_root();
}

SceneTree *SpxManager::get_tree() {
	// 直接调用方：派生 Manager 查询或切换场景；顶层调用方：Go 侧场景 API。
	return SpxEngine::get_singleton()->get_tree();
}
