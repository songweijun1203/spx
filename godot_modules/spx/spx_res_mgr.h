/**************************************************************************/
/*  spx_platform_mgr.h                                                       */
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

#ifndef SPX_RES_MGR_H
#define SPX_RES_MGR_H

#include "gdextension_spx_ext.h"
#include "scene/resources/font.h"
#include "scene/resources/sprite_frames.h"
#include "servers/audio/audio_stream.h"
#include "spx_manager.h"
#include "spx_svg_cache.h"

class Texture2D;

namespace ProjectFonts {
struct Prepared;
}

// 动画 JSON 的校验后中间数据，由 create_animation 在一次构建过程中临时持有。
struct AnimPayload {
	String base_path; // 图集动画的底图路径；逐帧动画为空。
	Array frames; // 原始帧字典数组，生命周期由 Godot Variant 引用计数管理。
	int64_t max_bitmap = 1; // SVG 设计尺寸基准，用于推导逐帧栅格倍率。
};

// 一条可复用的 SPX 动画资源；纹理由 SpriteFrames 内的 Ref 共享。
struct SpxAnimationClip {
	Ref<SpriteFrames> frames; // 动画帧和时长的共享源资源。
	Vector<Vector2> offsets; // 与帧索引一一对应的服装轴心偏移。
	Vector<int> svg_frame_scales; // 每帧 SVG 相对 max_bitmap 的基础栅格倍率。
	bool is_svg = false; // 为 true 时按显示缩放从 SpxSvgCache 取得派生帧。
};

// SPX 资源管理器：统一路径转换、纹理/音频缓存、动画构建、SVG 栅格化和项目字体事务。
// SPX_BIND 接口的直接调用方是生成的 C/JS-WASM 桥；顶层调用方包括 Go game_build、
// Sprite 动画/服装、音频和资源查询 API。内部 load_* 也由 Sprite/UI/Audio/TileMap Manager 调用。
// Godot Resource 使用 Ref 引用计数；缓存持有强引用，并在 reset/destroy 时显式清理。
class SpxResMgr : public SpxManager {

private:
	HashMap<String, Ref<Texture2D>> cached_texture; // 引擎路径到位图纹理的强引用缓存。
	HashMap<String, Ref<AudioStream>> cached_audio; // 引擎路径到解码音频流的强引用缓存。
	HashMap<String, Ref<FontFile>> display_fonts; // 规范化 family 到 Godot UI 字体的缓存。
	Ref<Font> initial_theme_default_font; // 首次 awake 时保存的 Godot 默认字体，reset 时恢复。
	Ref<Font> initial_theme_fallback_font; // 首次 awake 时保存的 Godot fallback 字体。
	bool initial_theme_fonts_saved = false; // 防止多次 awake 覆盖最初主题字体快照。
	bool is_load_direct = true; // true 时通过 FileAccess/ImageLoader 读取外部游戏数据。
	String game_data_root = "res://"; // 相对素材路径的根目录，可指向解包后的外部目录。
	HashMap<String, SpxAnimationClip> animation_clips; // “精灵类型::动画名”到动态动画资源。
	SpxSvgCache svg_cache; // 本 Manager 独占的多倍率 SVG 图片/动画缓存。

private:
	Ref<Texture2D> _load_texture_direct(const String &p_path, bool p_allow_placeholder);
	Ref<AudioStream> _load_audio_direct(const String &p_path);

	bool _parse_anim_json(const String &src, bool p_is_atlas, AnimPayload &out);
	Vector2 _read_offset(const Dictionary &d);
	bool _build_normal_frames(const String &anim_key, const AnimPayload &payload,
			SpxAnimationClip &r_clip);
	bool _build_atlas_frames(const String &anim_key, const AnimPayload &payload,
			SpxAnimationClip &r_clip);
	void _commit_project_fonts(ProjectFonts::Prepared &&p_prepared);

public:
	// 生命周期由 SpxEngine 直接调用；顶层对应 Go 游戏初始化、重置和引擎销毁。
	void on_awake() override;
	void on_reset(int reset_code) override;
	void on_destroy() override;
	Ref<Texture2D> load_texture(String path, GdBool direct = false);
	Ref<Texture2D> load_texture_checked(const String &p_path,
			GdBool p_direct = false);
	Ref<AudioStream> load_audio(String path, GdBool direct = false);
	Ref<Texture2D> _reload_texture(String path);
	void set_game_datas(String path, Vector<String> files);
	void update_caches(const Vector<String> &files);
	bool has_animation(const String &p_key) const;
	Ref<SpriteFrames> get_animation_frames(const String &p_key, int p_raster_scale = 1);
	Ref<ImageTexture> load_svg_texture(const String &p_path, int p_raster_scale);
	String get_anim_key_name(const String &sprite_type_name, const String &anim_name);
	bool is_dynamic_anim_mode() const;
	bool is_svg_animation(const String &p_anim_key) const;
	Vector2 get_animation_frame_offset(String anim_key, int frame_index);
	String _to_engine_path(const String &p_path);

public:
	// 下列 SPX_BIND 方法由 ABI/Web 桥直接调用，顶层来自 Go ResMgr 和游戏构建流程。
	SPX_BIND void create_animation(GdString p_sprite_type, GdString p_anim_name, GdString p_json_ctx, GdInt fps, GdBool is_atlas);
	SPX_BIND void set_load_mode(GdBool is_direct_mode);
	SPX_BIND GdBool get_load_mode();
	SPX_BIND GdRect2 get_bound_from_alpha(GdString p_path);
	SPX_BIND GdVec2 get_image_size(GdString p_path);
	SPX_BIND GdString read_all_text(GdString p_path);
	SPX_BIND GdBool has_file(GdString p_path);
	SPX_BIND GdString list_directories(GdString p_path);
	SPX_BIND void reload_texture(GdString path);
	// 原子应用完整项目字体配置；成功返回已分配的空字符串，失败返回已分配的诊断文本。
	// Godot 字体主题和 LunaSVG 字体表必须在主线程整批发布，避免渲染线程看到半套配置。
	SPX_BIND GdString apply_project_fonts(GdString default_font_path, GdArray font_paths, GdArray font_families, GdArray preferences);
	SPX_BIND void set_default_font(GdString font_path);
	SPX_BIND void register_font_face(GdString font_path, GdString family);
	SPX_BIND void set_font_preferences(GdArray preferences);
};

#endif // SPX_RES_MGR_H
