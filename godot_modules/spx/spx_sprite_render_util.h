/**************************************************************************/
/*  spx_sprite_render_util.h                                              */
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

#ifndef SPX_SPRITE_RENDER_UTIL_H
#define SPX_SPRITE_RENDER_UTIL_H

#include "core/math/vector2.h"
#include "scene/resources/atlas_texture.h"
#include "scene/resources/sprite_frames.h"

// RenderRoot 持有服装中心偏移。单图模式由动画节点直接继承；多帧动画先抵消该偏移，
// 再应用逐帧元数据，使最终位置保持一致。直接调用方是 SpxSprite 渲染/动画实现。
static inline Vector2 spx_compute_anim_offset(bool p_is_single_image_mode, const Vector2 &p_base_offset, const Vector2 &p_render_offset, const Vector2 &p_frame_offset, const Vector2 &p_render_scale) {
	Vector2 final_offset = p_base_offset + (p_frame_offset * p_render_scale);
	if (!p_is_single_image_mode) {
		final_offset -= p_render_offset;
	}
	return final_offset;
}

// Godot 的 SpriteFrames 把 loop 存在资源上；为每个播放器复制所选片段的元数据，
// 但复制 Ref<Texture2D> 继续共享图像存储，避免一个精灵改循环状态影响另一个精灵。
static inline Ref<SpriteFrames>
spx_copy_animation_frames(const Ref<SpriteFrames> &p_frames,
		const StringName &p_animation) {
	ERR_FAIL_COND_V(p_frames.is_null() || !p_frames->has_animation(p_animation),
			Ref<SpriteFrames>());
	Ref<SpriteFrames> frames;
	frames.instantiate();
	frames->remove_animation("default");
	frames->add_animation(p_animation);
	frames->set_animation_speed(p_animation,
			p_frames->get_animation_speed(p_animation));
	frames->set_animation_loop(p_animation,
			p_frames->get_animation_loop(p_animation));
	for (int i = 0; i < p_frames->get_frame_count(p_animation); i++) {
		frames->add_frame(p_animation, p_frames->get_frame_texture(p_animation, i),
				p_frames->get_frame_duration(p_animation, i));
	}
	return frames;
}

// 返回动画帧的归一化图集区域。它属于 SPX Shader 渲染策略，因此留在模块内，
// 不扩展 Godot AnimatedSprite2D 核心 API。
static inline Rect2 spx_get_animation_frame_uv_rect(const Ref<SpriteFrames> &p_frames, const StringName &p_animation, int p_frame) {
	const Rect2 default_uv(0, 0, 1, 1);
	if (p_frames.is_null() || !p_frames->has_animation(p_animation) ||
			p_frame < 0 || p_frame >= p_frames->get_frame_count(p_animation)) {
		return default_uv;
	}

	const Ref<Texture2D> texture = p_frames->get_frame_texture(p_animation, p_frame);
	if (texture.is_null()) {
		return default_uv;
	}

	const Ref<AtlasTexture> atlas_texture = Object::cast_to<AtlasTexture>(texture.ptr());
	if (atlas_texture.is_null() || atlas_texture->get_atlas().is_null()) {
		return default_uv;
	}

	const Size2 atlas_size = atlas_texture->get_atlas()->get_size();
	if (atlas_size.x <= 0.0f || atlas_size.y <= 0.0f) {
		return default_uv;
	}

	const Rect2 region = atlas_texture->get_region();
	return Rect2(region.position / atlas_size, region.size / atlas_size);
}

#endif // SPX_SPRITE_RENDER_UTIL_H
