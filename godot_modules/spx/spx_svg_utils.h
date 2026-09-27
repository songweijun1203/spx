/**************************************************************************/
/*  spx_svg_utils.h                                                       */
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

#ifndef SPX_SVG_UTILS_H
#define SPX_SVG_UTILS_H

#include "core/string/ustring.h"
#include "core/templates/vector.h"

// 一张供 LunaSVG 使用的项目字体面；字节由快照持有，不依赖外部文件寿命。
struct SpxSvgProjectFontFace {
	String family; // CSS font-family 原始名称。
	Vector<uint8_t> data; // 完整字体文件字节，发布时复制进进程级注册表。
};

// LunaSVG 字体注册桥：维护进程级不可分割快照，并在各渲染线程惰性安装本地字体表。
// 直接调用方是 SpxResMgr 字体提交和 SVG 加载器；顶层调用方是 Go 项目字体初始化。
// LunaSVG 的字体注册是线程局部的，因此每个实际渲染线程都必须调用 ensure_*。
class SpxSvgUtils {
public:
	// 用 LunaSVG 相同解析路径校验字体字节，但不修改当前线程缓存或进程级项目字体表。
	static bool is_font_data_valid(const Vector<uint8_t> &font_data);
	// 原子替换完整项目字体快照；渲染线程只会看到旧代或完整新代，不会看到逐张注册的中间态。
	static void apply_font_registry(const Vector<uint8_t> &default_font_data, const Vector<SpxSvgProjectFontFace> &named_font_faces, const Vector<String> &preferences);
	static void set_default_font(const void *font_data, int length);
	static void add_font_face(const String &family, const void *font_data, int length);
	static void set_font_preferences(const Vector<String> &preferences);
	static void reset_font_registry();
	// 当前线程无法完整安装快照时返回 false；调用方不得使用部分字体代继续渲染。
	static bool ensure_font_faces_registered();
};

#endif // SPX_SVG_UTILS_H
