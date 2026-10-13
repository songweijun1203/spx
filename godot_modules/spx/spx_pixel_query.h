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
#include <cstdint>
#include <unordered_map>
#include <vector>

namespace SpxPixelQuery {

// 单次像素查询快照：保存当前帧纹理、世界变换和采样参数，不拥有 Manager 或节点。
// 图像只在包围盒相交后惰性读取，Godot Ref 保证查询期间资源存活。
struct Snapshot {
	Ref<Texture2D> texture; // 当前动画帧纹理。
	Ref<Image> image; // 惰性取得的 CPU 像素。
	Rect2 bounds; // 全局变换后的世界轴对齐包围盒。
	Rect2 local_rect; // 考虑 centered/offset 后的局部矩形。
	Transform2D inverse_transform; // 世界坐标到局部坐标的逆变换。
	Vector2i image_size; // CPU 图像尺寸，加载前暂取纹理尺寸。
	real_t collision_alpha_scale = 1.0f; // 幽灵等效果对碰撞 alpha 的缩放。
	bool flip_h = false; // 水平镜像采样。
	bool flip_v = false; // 垂直镜像采样。
	bool image_premultiplied = false; // 渲染目标为预乘 RGB，普通服装不是。
};

struct ImageEntry {
	// texture 让作为哈希 key 的裸指针在本轮查询期间保持有效。
	Ref<Texture2D> texture;
	Ref<Image> image;
	// SpxImageTexture 内容版本变化时，即使对象指针相同也必须重新取图。
	uint64_t version;
};

// 一次场景颜色/像素碰撞操作内共享的 CPU 图像缓存，不跨查询长期持有。
using ImageCache = std::unordered_map<const Texture2D *, ImageEntry>;

// 可参与场景颜色合成的一层；按 z、舞台类别和场景树顺序判定前后关系。
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
bool load_image(Snapshot &r_snapshot, ImageCache *p_cache = nullptr);

// Select world pixel centers, not pixel edges. Sampling is x-major and keeps
// its origin at this rectangle's first center even when the step exceeds one.
Rect2i pixel_centers(const Rect2 &p_bounds);
Rect2i overlap(const Rect2 &p_a, const Rect2 &p_b);
template <typename Predicate>
bool any_pixel_center(const Rect2i &p_rect, int p_step, const Predicate &p_matches) {
	// Callers clamp p_step to at least one when setting the sampling policy.
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
