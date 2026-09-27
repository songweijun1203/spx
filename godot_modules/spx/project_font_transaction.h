/**************************************************************************/
/*  project_font_transaction.h                                            */
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

#ifndef PROJECT_FONT_TRANSACTION_H
#define PROJECT_FONT_TRANSACTION_H

#include "core/templates/hash_set.h"
#include "gdextension_spx_ext.h"
#include "scene/resources/font.h"
#include "spx_svg_utils.h"

class SpxResMgr;

namespace ProjectFonts {

// 一张待加载字体的用户配置；family_key 用于大小写无关的唯一性校验和查表。
struct FaceSpec {
	String path; // Go 项目配置中的资源路径。
	String family; // 提供给 Godot/LunaSVG 的显示族名。
	String family_key; // ASCII 归一化后的内部键。
};

// 从 ABI 数组解码出的完整字体事务请求，尚未访问文件系统。
struct Request {
	String default_path; // 必选的默认字体路径。
	Vector<FaceSpec> faces; // 额外命名字体列表。
	Vector<String> preferences; // fallback 优先级，元素为规范化 family 键。
};

// 已完成所有易失败工作的提交包；只有 prepare 全部成功后才交给 SpxResMgr 发布。
struct Prepared {
	Vector<uint8_t> default_data; // LunaSVG 默认字体的完整字节。
	Vector<SpxSvgProjectFontFace> faces; // LunaSVG 命名字体快照。
	HashMap<String, Ref<FontFile>> display_fonts; // Godot UI 使用的字体强引用表。
	Ref<Font> theme_font; // 按 preferences 组装后的 Godot fallback 链。
	Vector<String> preferences; // 校验并规范化后的族名顺序。
};

// 字体事务工具的直接调用方是 SpxResMgr::apply_project_fonts；顶层调用方是 Go
// game_build -> applyRuntimeFontPlan。准备阶段不修改全局主题，提交阶段必须在主线程完成。

String fold_family(const String &p_family);
bool strings_from_array(GdArray p_values, const String &p_name, Vector<String> &r_values, String &r_error);
bool validate_preferences(const Vector<String> &p_preferences, const HashSet<String> &p_families, String &r_error);
bool prepare_font(const String &p_path, SpxResMgr &p_resources, Vector<uint8_t> &r_data, Ref<FontFile> &r_font, String &r_error);
Ref<Font> build_display_font_chain(const HashMap<String, Ref<FontFile>> &p_fonts, const Vector<String> &p_preferences);
bool decode_request(GdString p_default_font_path, GdArray p_font_paths, GdArray p_font_families, GdArray p_preferences, Request &r_request, String &r_error);
bool validate_request(Request &r_request, String &r_error);
bool prepare(const Request &p_request, SpxResMgr &p_res_mgr, Prepared &r_prepared, String &r_error);

} // namespace ProjectFonts

#endif // PROJECT_FONT_TRANSACTION_H
