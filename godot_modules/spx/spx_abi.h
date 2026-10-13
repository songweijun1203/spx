/**************************************************************************/
/*  spx_abi.h                                                             */
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

#ifndef SPX_ABI_H
#define SPX_ABI_H

#include "gdextension_spx_ext.h"
#include <type_traits>

// 把 ABI 借用的 UTF-8 C 字符串复制成 Godot String；直接调用方：各 SPX_BIND 实现。
#define SpxStr(str) (String::utf8((const char *)str))
// 把 Godot String 复制为调用方拥有的 ABI 返回字符串；调用方必须调用 free_return_cstr。
#define SpxReturnStr(str) (SpxAbi::to_return_cstr(str))
// 跨语言对象 ID 的空值；有效 ID 从 1 开始。
#define NULL_OBJECT_ID 0

// 标记由绑定生成器导出的跨语言方法；静态 C++ 声明不需要 Manager 实例。
#define SPX_BIND

// 标记只写 Native 数组：成功时完整填充，入口不读取；失败时 GdBool 方法在写数组前返回 false。
#define SPX_OUT

// SPX 跨 Go/C++/WASM 边界的内存适配层。
// 本命名空间创建的字符串和数组归调用方所有，与 SpxEngine 生命周期无关；即使引擎已 shutdown，
// 调用方仍可使用对应 free 函数释放。顶层调用方是 Go gdengine binding 或 Web JS bridge。
namespace SpxAbi {

// 校验描述符、索引、线类型和元素大小后返回元素借用地址；仅供 get_array 模板调用。
void *_get_array(GdArray array, int64_t index, int type_size,
		int32_t expected_type);

// 把 C++ 元素类型映射到稳定 GdArrayType；映射必须与 Go/JS 绑定生成器保持一致。
template <typename T>
constexpr int32_t _array_type_for() {
	using U = std::remove_cv_t<std::remove_reference_t<T>>;
	if constexpr (std::is_same_v<U, GdString> ||
			(std::is_pointer_v<U> &&
					std::is_same_v<std::remove_cv_t<std::remove_pointer_t<U>>,
							char>)) {
		return GD_ARRAY_TYPE_STRING;
	} else if constexpr (std::is_floating_point_v<U>) {
		return GD_ARRAY_TYPE_FLOAT;
	} else if constexpr (std::is_same_v<U, bool> ||
			(std::is_integral_v<U> &&
					sizeof(U) == sizeof(uint8_t))) {
		// GdBool 与 byte 都是一字节 ABI 值，底层 C 别名可能无法区分；
		// 因此 _get_array 对这一类别同时接受 BOOL/BYTE 线类型。
		return GD_ARRAY_TYPE_BOOL;
	} else if constexpr (std::is_integral_v<U> && sizeof(U) == sizeof(int64_t)) {
		// GdInt 和 GdObj 按协议有意共用 64 位 ABI。
		return GD_ARRAY_TYPE_INT64;
	} else {
		return GD_ARRAY_TYPE_UNKNOWN;
	}
}

// 分配并复制返回字符串。直接调用方：SpxReturnStr；顶层调用方：Go 的字符串返回值 API。
GdString to_return_cstr(const String &ret_val);
// 释放 to_return_cstr 的结果。直接调用方：生成的全局 free_string 接口；顶层调用方：Go binding。
void free_return_cstr(GdString ret_val);
// 创建指定线类型和长度的零初始化数组；返回值归调用方所有。
GdArray create_array(int32_t type, int32_t size);
// 释放 create_array 创建的描述符、数据及其拥有的字符串元素，可在引擎关闭后调用。
void free_array(GdArray array);

template <typename T>
// 经类型和边界校验后写入单个元素；失败时保持数组不变。
void set_array(GdArray array, int64_t index, T value);
// 返回数组元素的借用指针；数组释放后立即失效，调用方不得跨 ABI 调用保存。
template <typename T>
T *get_array(GdArray array, int64_t index);

template <typename T>
T *get_array(GdArray array, int64_t index) {
	return static_cast<T *>(
			_get_array(array, index, sizeof(T), _array_type_for<T>()));
}
template <typename T>
void set_array(GdArray array, int64_t index, T value) {
	auto ptr = get_array<T>(array, index);
	if (ptr == nullptr) {
		return;
	}
	*ptr = value;
}

} // namespace SpxAbi

#endif // SPX_ABI_H
