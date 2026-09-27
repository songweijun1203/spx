/**************************************************************************/
/*  spx_batch_validation.h                                                */
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

#ifndef SPX_BATCH_VALIDATION_H
#define SPX_BATCH_VALIDATION_H

#include <cmath>
#include <limits>

namespace SpxBatchValidation {

// 精灵批量同步协议的数值校验工具。
// 为兼容既有桥，批次头也通过 float32 传输；转成 int 前必须拒绝 NaN、无穷、非整数和越界值，
// 因为 C++ 把这些浮点值直接转为 int 会产生未定义行为。
// 直接调用方：spx_sprite_batch.cpp 的解析器；顶层调用方：Go runtime_sync 批量同步。
static inline bool decode_int(float p_value, int &r_value) {
	const double value = static_cast<double>(p_value);
	if (!std::isfinite(value) || std::trunc(value) != value ||
			value < static_cast<double>(std::numeric_limits<int>::min()) ||
			value > static_cast<double>(std::numeric_limits<int>::max())) {
		return false;
	}

	r_value = static_cast<int>(value);
	return true;
}

// 解码元素数量等必须非负的整数字段；失败时调用方不得继续按该数量读取缓冲区。
static inline bool decode_nonnegative_count(float p_value, int &r_value) {
	return decode_int(p_value, r_value) && r_value >= 0;
}

// 验证一段连续浮点参数均为有限值，防止 NaN/Inf 进入 Godot Transform/物理状态。
static inline bool all_finite(const float *p_values, int p_count) {
	for (int i = 0; i < p_count; i++) {
		if (!std::isfinite(p_values[i])) {
			return false;
		}
	}
	return true;
}

} // namespace SpxBatchValidation

#endif // SPX_BATCH_VALIDATION_H
