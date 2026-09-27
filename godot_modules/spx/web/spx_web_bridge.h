/**************************************************************************/
/*  spx_web_bridge.h                                                      */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
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

#ifndef SPX_WEB_BRIDGE_H
#define SPX_WEB_BRIDGE_H

#include "core/math/vector2i.h"

// Web 平台 C++ 入口：安装 Godot C++ -> Emscripten JS Library -> Go WASM 的回调表。
// 直接调用方：initialize_spx_module(CORE)；顶层调用方：Engine.start -> Module.callMain。
// Emscripten 规则：这些函数只在 Web 构建链接，JS 导入符号必须已合并进最终 Module。
void spx_web_register_callbacks();

// 直接调用方：SPX 平台/窗口初始化；顶层调用方：Go 对舞台尺寸的查询。
// 浏览器 Canvas 尺寸由 JS 宿主掌握，因此经导入函数读取，不能假设等于 DisplayServer 尺寸。
Size2i spx_web_get_window_size();

#endif // SPX_WEB_BRIDGE_H
