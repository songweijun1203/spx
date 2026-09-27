#ifndef SPX_IMAGE_TEXTURE_H
#define SPX_IMAGE_TEXTURE_H

#include "core/object/callable_method_pointer.h"
#include "scene/resources/bit_map.h"
#include "scene/resources/image_texture.h"

// 带 CPU 像素快照的 ImageTexture，供像素碰撞、图集切片和瓦片复制读取。
// 直接调用方是 SpxResMgr/SpxSvgCache；顶层需求来自 Go 的服装加载、颜色感知和地图系统。
// Godot 规则：Ref 管理 Resource 引用计数；本类不进场景树，也不需要调用 queue_free()。
// CPU 数据随纹理实例所有，不依赖全局缓存或 Resource 元数据。
class SpxImageTexture : public ImageTexture {
	mutable Ref<Image> cpu_image; // 最近一次上传的 CPU 图像；mutable 允许 const get_image 惰性补齐。
	mutable Ref<BitMap> cpu_alpha_cache; // 按需生成的 alpha 位图，加速 is_pixel_opaque。
	bool uploading_image = false; // 模块主动上传期间抑制 changed 信号清空 cpu_image。

	void _invalidate_image() {
		if (!uploading_image) {
			cpu_image.unref();
		}
		cpu_alpha_cache.unref();
	}

	void _replace_image(const Ref<Image> &p_image) {
		cpu_image = p_image->duplicate();
		uploading_image = true;
		ImageTexture::set_image(p_image);
		uploading_image = false;
	}

public:
	SpxImageTexture() {
		// Godot Resource 内容变化会发出 changed；外部修改时让 CPU 快照失效。
		connect("changed", callable_mp(this, &SpxImageTexture::_invalidate_image));
	}

	static Ref<ImageTexture> create_from_image(const Ref<Image> &p_image) {
		ERR_FAIL_COND_V(p_image.is_null() || p_image->is_empty(), Ref<ImageTexture>());
		Ref<SpxImageTexture> texture;
		texture.instantiate();
		texture->_replace_image(p_image);
		return texture;
	}

	static void replace_image(const Ref<ImageTexture> &p_texture, const Ref<Image> &p_image) {
		ERR_FAIL_COND(p_texture.is_null() || p_image.is_null() || p_image->is_empty());
		// 此私有资源子类有意不声明 GDCLASS；使用 C++ RTTI 区分普通 ImageTexture，
		// 避免对未注册 Godot 类型做不安全的 Object::cast_to。
		if (SpxImageTexture *texture = dynamic_cast<SpxImageTexture *>(p_texture.ptr())) {
			texture->_replace_image(p_image);
		} else {
			p_texture->set_image(p_image);
		}
	}

	bool is_pixel_opaque(int p_x, int p_y) const override {
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
		// SPX 上传路径之外的继承接口修改会使快照失效；即使 Dummy 渲染后端无法回读 GPU，
		// SPX 热重载仍直接保留已上传的 CPU 像素。
		if (cpu_image.is_null()) {
			cpu_image = ImageTexture::get_image();
		}
		// Image::duplicate 在写入前共享像素存储，以低成本保持 get_image 返回值可独立修改。
		return cpu_image.is_valid() ? Ref<Image>(cpu_image->duplicate()) : Ref<Image>();
	}
};

#endif // SPX_IMAGE_TEXTURE_H
