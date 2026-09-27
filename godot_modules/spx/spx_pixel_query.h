/**************************************************************************/
/*  spx_pixel_query.h                                                    */
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

#ifndef SPX_PIXEL_QUERY_H
#define SPX_PIXEL_QUERY_H

#include "scene/2d/animated_sprite_2d.h"
#include <vector>

namespace SpxPixelQuery {

// 单次像素查询快照：保存精灵当前帧、世界变换和采样参数，不拥有 Manager 或节点。
// 直接调用方是 SpxSpriteMgr 的颜色感知/像素碰撞；顶层调用方是 Go Sprite.Touching、
// 颜色检测和点击命中流程。纹理与图像用 Godot Ref 强引用，图像只在包围盒相交后惰性读取。
struct Snapshot {
	Ref<Texture2D> texture; // 当前动画帧纹理的强引用，保证查询期间资源不被释放。
	Ref<Image> image; // 从 texture 惰性取得的 CPU 像素副本。
	Rect2 bounds; // 当前帧经全局变换后的世界轴对齐包围盒。
	Rect2 local_rect; // 考虑 AnimatedSprite2D 居中与 offset 后的局部矩形。
	Transform2D inverse_transform; // 世界坐标映射回精灵局部坐标的逆变换。
	Vector2i image_size; // CPU 图像像素尺寸；加载前暂取纹理尺寸。
	real_t collision_alpha_scale = 1.0f; // Shader alpha 效果对碰撞透明度的缩放。
	bool flip_h = false; // 采样时是否水平镜像。
	bool flip_v = false; // 采样时是否垂直镜像。
	bool image_premultiplied = false; // 渲染目标使用预乘 RGB，普通服装图像不预乘。
};

// 可参与场景颜色合成的一层；按 z、舞台类型和场景树顺序判定前后关系。
struct Layer {
	enum Order {
		BACKDROP,
		PEN,
		SPRITE,
	};

	Snapshot pixel_query; // 本层的像素查询快照及资源强引用。
	int z_index = 0; // Godot CanvasItem z_index，数值越大越靠前。
	Order order = SPRITE; // 同 z 时的 SPX 舞台绘制顺序。
	int tree_index = 0; // 同类型同 z 时的场景树顺序，越后越靠前。
	bool in_front_of(const Layer &p_other) const;
};

Ref<Texture2D> frame_texture(AnimatedSprite2D *p_animation);
Rect2 local_rect(AnimatedSprite2D *p_animation, const Vector2 &p_texture_size);
Rect2 world_bounds(const Transform2D &p_transform, const Rect2 &p_local_rect);
bool capture(AnimatedSprite2D *p_animation, bool p_apply_collision_alpha, Snapshot &r_snapshot);
bool load_image(Snapshot &r_snapshot);

// 选择世界像素中心而非像素边界；按 x 优先遍历，步长大于 1 时仍从首个中心开始。
Rect2i pixel_centers(const Rect2 &p_bounds);
Rect2i overlap(const Rect2 &p_a, const Rect2 &p_b);
template <typename Predicate>
bool any_pixel_center(const Rect2i &p_rect, int p_step, const Predicate &p_matches) {
	// 调用方在设置采样策略时已把 p_step 限制为至少 1。
	const Vector2i end = p_rect.position + p_rect.size;
	for (int x = p_rect.position.x; x < end.x; x += p_step) {
		for (int y = p_rect.position.y; y < end.y; y += p_step) {
			if (p_matches(Vector2((real_t)x + 0.5f, (real_t)y + 0.5f))) {
				return true;
			}
		}
	}
	return false;
}

// 调用前 load_image 必须成功。先应用逆变换、轴心/居中和翻转，再向下取整，
// 从而保留负缩放精灵的正确采样行为。
bool sample(const Snapshot &p_snapshot, const Vector2 &p_world_pos, Color &r_color);
bool sample_premultiplied(const Snapshot &p_snapshot, const Vector2 &p_world_pos, Color &r_color);

// 输入层按前到后排列（z、舞台顺序、树索引均降序）；按 Scratch 规则将预乘色叠到白底。
Color composite(const std::vector<Layer> &p_layers, const Vector2 &p_world_pos);

} // namespace SpxPixelQuery

#endif // SPX_PIXEL_QUERY_H
