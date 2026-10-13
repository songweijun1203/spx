/**************************************************************************/
/*  spx_ext_mgr.cpp                                                    */
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

#include "spx_ext_mgr.h"

#include "spx.h"
#include "spx_engine.h"
#include "spx_layer_sorter.h"

void SpxExtMgr::request_exit(GdInt exit_code) {
	// 直接调用方：Native/Web 生成 bridge；顶层调用方：Go runtime RequestExit。
	// Godot 规则：SceneTree::quit 只应在主线程调用；本入口不像 reset/pause 那样自动转投邮箱，
	// 因而绑定层必须保证 request_exit 在 Godot 主线程执行。
	auto callback = SpxEngine::get_singleton()->get_on_runtime_exit();
	if (callback != nullptr) {
		callback(exit_code);
	}

	SpxEngine::get_singleton()->on_exit(exit_code);
	SpxEngine::get_singleton()->get_tree()->quit(exit_code);
}

void SpxExtMgr::request_reset(GdInt exit_code) {
	// 直接调用方：Native/Web bridge；顶层调用方：Go runtime reset 请求。
	Spx::reset(exit_code);
}

void SpxExtMgr::request_restart() {
	// 直接调用方：Native/Web bridge；顶层调用方：Go 新一局启动流程。
	Spx::restart();
}

void SpxExtMgr::on_runtime_panic(GdString msg) {
	// 直接调用方：Native/Web bridge；顶层调用方：Go runtime panic 处理。
	// msg 是同步借用字符串，回调若需保存必须自行复制。
	auto callback = SpxEngine::get_singleton()->get_on_runtime_panic();
	if (callback != nullptr) {
		callback(msg);
	}
}

// 暂停 API 统一转发给 Spx 层，由其处理主线程投递。
void SpxExtMgr::pause() {
	// 直接调用方：Native/Web bridge；顶层调用方：Go engine pause API。
	Spx::pause();
}

void SpxExtMgr::resume() {
	// 直接调用方：Native/Web bridge；顶层调用方：Go engine resume API。
	Spx::resume();
}

GdBool SpxExtMgr::is_paused() {
	// 直接调用方：Native/Web bridge；顶层调用方：Go engine 状态查询。
	return Spx::is_paused();
}

void SpxExtMgr::next_frame() {
	// 直接调用方：Native/Web bridge；顶层调用方：Go 调试单步 API。
	Spx::next_frame();
}

void SpxExtMgr::set_layer_sorter_mode(GdInt mode) {
	// 直接调用方：Native/Web bridge；顶层调用方：Go 图层排序配置。
	SpxLayerSorter::instance().set_mode((LayerSortMode)mode);
}
