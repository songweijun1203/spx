#ifndef SPX_IMAGE_TEXTURE_H
#define SPX_IMAGE_TEXTURE_H

#include "core/object/callable_method_pointer.h"
#include "scene/resources/bit_map.h"
#include "scene/resources/image_texture.h"

#include <cstdint>

// SPX 使用的 ImageTexture 子类：纹理存活期间保留 CPU 像素，并按内容版本失效
// alpha 位图及像素查询缓存。类不注册 GDCLASS，只在模块内部通过 RTTI 识别。
class SpxImageTexture : public ImageTexture {
	// cpu_image 是最后一次 SPX 上传或从基类惰性读取的像素副本。
	mutable Ref<Image> cpu_image;
	// cpu_alpha_cache 服务 Texture2D::is_pixel_opaque，纹理变化时必须一起失效。
	mutable Ref<BitMap> cpu_alpha_cache;
	// set_image 会发出 changed；该标志避免 SPX 自己上传时清掉刚保存的 cpu_image。
	bool uploading_image = false;
	// 每次内容变化递增，SpxPixelQuery::ImageCache 用它识别同指针的新像素。
	uint64_t content_version = 0;

	void _invalidate_image() {
		content_version++;
		if (!uploading_image) {
			cpu_image.unref();
		}
		cpu_alpha_cache.unref();
	}

	void _replace_image(const Ref<Image> &p_image) {
		// duplicate 保证调用者之后修改原 Image 不会静默改变查询使用的快照。
		cpu_image = p_image->duplicate();
		uploading_image = true;
		ImageTexture::set_image(p_image);
		uploading_image = false;
	}

public:
	uint64_t image_version() const { return content_version; }

	SpxImageTexture() {
		connect("changed", callable_mp(this, &SpxImageTexture::_invalidate_image));
	}

	static Ref<ImageTexture> create_from_image(const Ref<Image> &p_image) {
		// 返回基类 Ref，使其可直接进入现有 Godot 纹理 API。
		ERR_FAIL_COND_V(p_image.is_null() || p_image->is_empty(), Ref<ImageTexture>());
		Ref<SpxImageTexture> texture;
		texture.instantiate();
		texture->_replace_image(p_image);
		return texture;
	}

	static void replace_image(const Ref<ImageTexture> &p_texture, const Ref<Image> &p_image) {
		ERR_FAIL_COND(p_texture.is_null() || p_image.is_null() || p_image->is_empty());
		// 无 GDCLASS：RTTI 安全地区分内部子类；普通 ImageTexture 沿用基类更新。
		if (SpxImageTexture *texture = dynamic_cast<SpxImageTexture *>(p_texture.ptr())) {
			texture->_replace_image(p_image);
		} else {
			p_texture->set_image(p_image);
		}
	}

	bool is_pixel_opaque(int p_x, int p_y) const override {
		// 第一次查询时构建 alpha 位图，后续按纹理坐标比例复用。
		if (cpu_alpha_cache.is_null()) {
			Ref<Image> image = get_image();
			if (image.is_null()) {
				return true;
			}
			if (image->is_compressed()) {
				image->decompress();
			}
			cpu_alpha_cache.instantiate();
			cpu_alpha_cache->create_from_image_alpha(image);
		}
		const Size2i size = cpu_alpha_cache->get_size();
		if (size.x <= 0 || size.y <= 0 || get_width() <= 0 || get_height() <= 0) {
			return true;
		}
		return cpu_alpha_cache->get_bit(CLAMP(p_x * size.x / get_width(), 0, size.x - 1), CLAMP(p_y * size.y / get_height(), 0, size.y - 1));
	}

	Ref<Image> get_image() const override {
		// 非 SPX 路径修改的纹理由基类惰性回读；SPX 上传已经保留 CPU 像素。
		if (cpu_image.is_null()) {
			cpu_image = ImageTexture::get_image();
		}
		// duplicate 使用写时复制，使返回 Image 可独立修改而不污染缓存。
		return cpu_image.is_valid() ? Ref<Image>(cpu_image->duplicate()) : Ref<Image>();
	}
};

#endif // SPX_IMAGE_TEXTURE_H
