#include "spx_svg_cache.h"

#include "core/math/math_funcs.h"
#include "core/os/thread.h"
#include "spx_image_loader_svg.h"
#include "spx_image_texture.h"

bool SpxSvgCache::is_svg_path(const String &p_path) {
	return p_path.get_extension().nocasecmp_to("svg") == 0;
}

int SpxSvgCache::raster_scale(Vector2 p_required_scale) {
	return raster_scale(MAX(Math::abs(p_required_scale.x), Math::abs(p_required_scale.y)));
}

int SpxSvgCache::raster_scale(float p_required_scale) {
	const float required_scale = Math::abs(p_required_scale);
	// 使用 Scratch 风格的 SVG MIP 倍率，并限制在支持的最大栅格倍率内。
	int scale = 1;
	while (float(scale) < required_scale && scale < 1024) {
		scale <<= 1;
	}
	return scale;
}

Ref<ImageTexture> SpxSvgCache::load_image(const String &p_path, int p_scale) {
	// 直接调用方：SpxResMgr::load_svg_texture；顶层来自 Go 服装/纹理切换。
	// 每个倍率单独缓存，避免高分辨率显示时反复栅格化或低倍率放大模糊。
	ERR_FAIL_COND_V_MSG(!Thread::is_main_thread(), Ref<ImageTexture>(), "SVG caches may only be accessed on the main thread.");
	ERR_FAIL_COND_V(p_scale < 1 || !is_svg_path(p_path), Ref<ImageTexture>());
	const auto *scales = images.getptr(p_path);
	if (scales != nullptr) {
		const Ref<ImageTexture> *cached = scales->getptr(p_scale);
		if (cached != nullptr) {
			return *cached;
		}
	}
	Ref<Image> image;
	image.instantiate();
	if (SpxImageLoaderSVG::load_image(p_path, image, ImageFormatLoader::FLAG_NONE, p_scale) != OK) {
		return Ref<ImageTexture>();
	}
	Ref<ImageTexture> texture = SpxImageTexture::create_from_image(image);
	texture->set_path_cache(p_path);
	images[p_path].insert(p_scale, texture);
	return texture;
}

Ref<ImageTexture> SpxSvgCache::reload_image(const String &p_path) {
	// 对每个已缓存倍率原位更新纹理，现有节点持有的 Ref 无需重新绑定。
	ERR_FAIL_COND_V_MSG(!Thread::is_main_thread(), Ref<ImageTexture>(), "SVG caches may only be accessed on the main thread.");
	HashMap<int, Ref<Image>> prepared;
	prepared.insert(1, Ref<Image>());
	auto *scales = images.getptr(p_path);
	if (scales != nullptr) {
		for (const auto &entry : *scales) {
			prepared.insert(entry.key, Ref<Image>());
		}
	}
	for (auto &entry : prepared) {
		entry.value.instantiate();
		if (SpxImageLoaderSVG::load_image(p_path, entry.value, ImageFormatLoader::FLAG_NONE, entry.key) != OK) {
			const Ref<ImageTexture> *old = scales != nullptr ? scales->getptr(1) : nullptr;
			return old != nullptr ? *old : Ref<ImageTexture>();
		}
	}
	// 一次发布所有已缓存倍率，并为精灵和动画片段保持纹理对象身份不变。
	for (const auto &entry : prepared) {
		auto &cached_scales = images[p_path];
		Ref<ImageTexture> *texture = cached_scales.getptr(entry.key);
		if (texture != nullptr) {
			SpxImageTexture::replace_image(*texture, entry.value);
		} else {
			Ref<ImageTexture> created = SpxImageTexture::create_from_image(entry.value);
			created->set_path_cache(p_path);
			cached_scales.insert(entry.key, created);
		}
	}
	return images[p_path][1];
}

Ref<SpriteFrames> SpxSvgCache::load_animation(const String &p_key, const Ref<SpriteFrames> &p_source, const Vector<int> &p_frame_scales, int p_scale) {
	// 直接调用方：SpxResMgr::get_animation_frames；顶层来自 SpxSprite 的 SVG 动画缩放。
	ERR_FAIL_COND_V_MSG(!Thread::is_main_thread(), Ref<SpriteFrames>(), "SVG caches may only be accessed on the main thread.");
	ERR_FAIL_COND_V(p_scale < 1 || p_source.is_null() || !p_source->has_animation(p_key), Ref<SpriteFrames>());
	const int frame_count = p_source->get_frame_count(p_key);
	ERR_FAIL_COND_V(p_frame_scales.size() != frame_count, Ref<SpriteFrames>());
	const auto *scales = animations.getptr(p_key);
	if (scales != nullptr) {
		const Ref<SpriteFrames> *cached = scales->getptr(p_scale);
		if (cached != nullptr) {
			return *cached;
		}
	}
	Ref<SpriteFrames> frames;
	frames.instantiate();
	frames->remove_animation("default");
	frames->add_animation(p_key);
	frames->set_animation_loop(p_key, p_source->get_animation_loop(p_key));
	frames->set_animation_speed(p_key, p_source->get_animation_speed(p_key));
	for (int i = 0; i < frame_count; i++) {
		const Ref<Texture2D> source_texture = p_source->get_frame_texture(p_key, i);
		ERR_FAIL_COND_V(source_texture.is_null(), Ref<SpriteFrames>());
		// 即使 1 倍动画也通过图片缓存解析，使失效操作能替换原动画元数据持有的 SVG 像素。
		const Ref<ImageTexture> texture = load_image(source_texture->get_path(), p_scale * p_frame_scales[i]);
		if (texture.is_null()) {
			return Ref<SpriteFrames>();
		}
		frames->add_frame(p_key, texture, p_source->get_frame_duration(p_key, i));
	}
	animations[p_key].insert(p_scale, frames);
	return frames;
}

void SpxSvgCache::invalidate_image(const String &p_path) {
	ERR_FAIL_COND_MSG(!Thread::is_main_thread(), "SVG caches may only be accessed on the main thread.");
	if (is_svg_path(p_path)) {
		images.erase(p_path);
		// 同一动画片段可能在任意栅格倍率使用发生变化的图片。
		animations.clear();
	}
}

void SpxSvgCache::clear() {
	ERR_FAIL_COND_MSG(!Thread::is_main_thread(), "SVG caches may only be accessed on the main thread.");
	images.clear();
	animations.clear();
}
