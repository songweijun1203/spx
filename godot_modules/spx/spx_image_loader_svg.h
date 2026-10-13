/**************************************************************************/
/*  spx_image_loader_svg.h                                                */
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

#ifndef SPX_IMAGE_LOADER_SVG_H
#define SPX_IMAGE_LOADER_SVG_H

#include "core/io/image_loader.h"

// LunaSVG 栅格化适配器：把 SVG 文件/UTF-8 文本转成 Godot Image，并支持主题色替换。
// 直接调用方是 SpxSvgCache；顶层调用方是 SpxResMgr，再上层是 Go 服装/动画加载。
// Image 是 RefCounted 资源，调用方持有输出 Ref；本类无实例状态，接口可被渲染工作线程调用。
class SpxImageLoaderSVG {
	static HashMap<Color, Color> forced_color_map; // 进程级默认颜色替换表；设置阶段与读取阶段不得并发修改。

	// 替换 fill/stroke 等 SVG 属性；只改本次解析的字符串副本。
	static void _replace_color_property(const HashMap<Color, Color> &p_color_map, const String &p_prefix, String &r_string);

public:
	static void set_forced_color_map(const HashMap<Color, Color> &p_color_map);

	static Error create_image_from_utf8_buffer(Ref<Image> p_image, const uint8_t *p_buffer, int p_buffer_size, float p_scale, bool p_upsample);
	static Error create_image_from_utf8_buffer(Ref<Image> p_image, const PackedByteArray &p_buffer, float p_scale, bool p_upsample);

	static Error create_image_from_string(Ref<Image> p_image, String p_string, float p_scale, bool p_upsample, const HashMap<Color, Color> &p_color_map);

	static Error load_image(Ref<Image> p_image, Ref<FileAccess> p_fileaccess, BitField<ImageFormatLoader::LoaderFlags> p_flags, float p_scale);
	static Error load_image(const String &p_path, Ref<Image> p_image, BitField<ImageFormatLoader::LoaderFlags> p_flags = ImageFormatLoader::FLAG_NONE, float p_scale = 1.0f);
};

#endif // SPX_IMAGE_LOADER_SVG_H
