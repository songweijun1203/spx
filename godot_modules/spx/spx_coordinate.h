/**************************************************************************/
/*  spx_coordinate.h                                                      */
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

#ifndef SPX_COORDINATE_H
#define SPX_COORDINATE_H

#include "core/math/vector2.h"

// SPX 坐标系 Y 轴向上，Godot 2D 坐标系 Y 轴向下。Manager 的 ABI 对外交换 SPX
// 坐标，应在边界立即转换，确保 Godot Node、渲染和物理 API 始终只接收 Godot 坐标。
// 直接调用方：Camera/Input/Physics/Navigation/Debug 等 Manager；
// 顶层调用方：Go 侧所有涉及位置、方向、射线和路径的 API。
_FORCE_INLINE_ Vector2 spx_to_godot_vec2(const Vector2 &p_value) {
	return Vector2(p_value.x, -p_value.y);
}

// Y 轴翻转是自反变换，因此复用上面的实现；保留独立函数名用于明确调用点方向。
_FORCE_INLINE_ Vector2 godot_to_spx_vec2(const Vector2 &p_value) {
	return spx_to_godot_vec2(p_value);
}

#endif // SPX_COORDINATE_H
