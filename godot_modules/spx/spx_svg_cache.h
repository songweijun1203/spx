#ifndef SPX_SVG_CACHE_H
#define SPX_SVG_CACHE_H

#include "core/math/vector2.h"
#include "core/templates/hash_map.h"
#include "scene/resources/image_texture.h"
#include "scene/resources/sprite_frames.h"

// SVG 多倍率缓存，由 SpxResMgr 独占。传入路径已转换为 Godot 引擎路径。
// 直接调用方是 SpxResMgr/SpxSprite；顶层需求来自 Go 的 SVG 服装、动画缩放和热重载。
// Ref 强引用保证纹理/帧资源在缓存或使用者仍持有时有效；clear 只释放缓存自己的引用。
class SpxSvgCache {
	HashMap<String, HashMap<int, Ref<ImageTexture>>> images; // 路径 -> 栅格倍率 -> 单图纹理。
	HashMap<String, HashMap<int, Ref<SpriteFrames>>> animations; // 动画键 -> 倍率 -> 派生帧资源。

public:
	static bool is_svg_path(const String &p_path);
	static int raster_scale(Vector2 p_required_scale);
	static int raster_scale(float p_required_scale);

	Ref<ImageTexture> load_image(const String &p_path, int p_scale);
	Ref<ImageTexture> reload_image(const String &p_path);
	Ref<SpriteFrames> load_animation(const String &p_key, const Ref<SpriteFrames> &p_source, const Vector<int> &p_frame_scales, int p_scale);
	void invalidate_image(const String &p_path);
	void clear();
};

#endif // SPX_SVG_CACHE_H
