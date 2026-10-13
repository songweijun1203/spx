/**************************************************************************/
/*  spx_svg_utils.cpp                                                     */
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

#include "spx_svg_utils.h"

#include "core/os/memory.h"
#include "core/os/mutex.h"
#include "core/templates/hash_map.h"
#include "core/templates/vector.h"
#include "servers/text_server.h"
#include "thirdparty/lunasvg/include/lunasvg.h"
#include "thirdparty/lunasvg/include/plutovg.h"

#include <lunasvg.h>
#ifdef LUNASVG_ENABLE_HARFBUZZ
#include <hb.h>
#endif
#include <algorithm>
#include <cstring>

namespace {

// 进程级注册表内部保存的一张规范化字体面。
struct StoredSVGFontFace {
	String family; // 已按 ASCII 小写折叠的 family 键。
	Vector<uint8_t> data; // 写时复制的完整字体字节。
};

static String _ascii_fold_font_family(const String &family) {
	String folded = family;
	for (int i = 0; i < folded.length(); i++) {
		char32_t character = folded[i];
		if (character >= U'A' && character <= U'Z') {
			folded[i] = character + (U'a' - U'A');
		}
	}
	return folded;
}

// 某一时刻的不可变字体注册快照，由渲染线程整批安装。
struct FontRegistrySnapshot {
	Vector<uint8_t> default_font_data; // 默认字体字节快照。
	Vector<StoredSVGFontFace> named_font_faces; // 命名字体面快照。
	Vector<String> font_preferences; // CSS family fallback 顺序。
	bool font_preferences_configured = false; // 区分“未配置”与“显式空列表”。
	uint64_t serial = 0; // 任意配置变化均递增，用于线程判断是否需要重装。
	uint64_t generation = 0; // reset/整批替换时递增，用于决定是否先清线程缓存。
};

// 进程级线程安全字体注册表；直接由 SpxSvgUtils 静态接口访问，顶层来自项目字体提交
// 与 SVG 栅格化；写入由 Godot 主线程发起，渲染线程只获取完整快照。
class FontRegistry {
public:
	void replace_all(const Vector<uint8_t> &p_default_font_data, const Vector<SpxSvgProjectFontFace> &p_named_font_faces, const Vector<String> &p_preferences) {
		HashMap<String, Vector<uint8_t>> named_font_faces;
		for (const SpxSvgProjectFontFace &face : p_named_font_faces) {
			named_font_faces.insert(_ascii_fold_font_family(face.family), face.data);
		}

		MutexLock lock(m_mutex);
		m_default_font_data = p_default_font_data;
		m_named_font_faces = named_font_faces;
		m_font_preferences = p_preferences;
		m_font_preferences_configured = true;
		m_generation++;
		m_serial++;
	}

	void replace_default_font(const Vector<uint8_t> &p_font_data) {
		MutexLock lock(m_mutex);
		m_default_font_data = p_font_data;
		m_serial++;
	}

	void replace_font_face(const String &p_family, const Vector<uint8_t> &p_font_data) {
		MutexLock lock(m_mutex);
		m_named_font_faces.insert(_ascii_fold_font_family(p_family), p_font_data);
		m_serial++;
	}

	void set_font_preferences(const Vector<String> &p_preferences) {
		MutexLock lock(m_mutex);
		m_font_preferences = p_preferences;
		m_font_preferences_configured = true;
		m_serial++;
	}

	void reset() {
		MutexLock lock(m_mutex);
		m_default_font_data.clear();
		m_named_font_faces.clear();
		m_font_preferences.clear();
		m_font_preferences_configured = false;
		m_generation++;
		m_serial++;
	}

	bool snapshot_if_changed(uint64_t p_applied_serial, FontRegistrySnapshot &r_snapshot) {
		MutexLock lock(m_mutex);
		if (m_serial == 0 || m_serial == p_applied_serial) {
			return false;
		}

		r_snapshot.default_font_data = m_default_font_data;
		r_snapshot.font_preferences = m_font_preferences;
		r_snapshot.font_preferences_configured = m_font_preferences_configured;
		r_snapshot.serial = m_serial;
		r_snapshot.generation = m_generation;
		r_snapshot.named_font_faces.clear();
		for (const KeyValue<String, Vector<uint8_t>> &E : m_named_font_faces) {
			StoredSVGFontFace stored_font;
			stored_font.family = E.key;
			stored_font.data = E.value;
			r_snapshot.named_font_faces.push_back(stored_font);
		}
		return true;
	}

private:
	Mutex m_mutex; // 保护以下所有进程级状态和快照复制。
	Vector<uint8_t> m_default_font_data; // 当前默认字体字节。
	HashMap<String, Vector<uint8_t>> m_named_font_faces; // 规范化 family 到字体字节。
	Vector<String> m_font_preferences; // 当前 fallback 偏好顺序。
	bool m_font_preferences_configured = false; // 是否曾显式配置偏好。
	uint64_t m_serial = 0; // 每次内容变化递增。
	uint64_t m_generation = 0; // reset 或完整替换时递增。
};

static FontRegistry &get_font_registry() {
	static FontRegistry registry;
	return registry;
}

static size_t _get_grapheme_breaks(const uint32_t *text, size_t length, size_t *breaks, size_t capacity, void *) {
	if (text == nullptr || length == 0 || breaks == nullptr || capacity == 0) {
		return 0;
	}
	TextServerManager *manager = TextServerManager::get_singleton();
	if (manager == nullptr) {
		return 0;
	}
	Ref<TextServer> text_server = manager->get_primary_interface();
	if (text_server.is_null() || !text_server->has_feature(TextServer::FEATURE_BREAK_ITERATORS)) {
		return 0;
	}

	String value(reinterpret_cast<const char32_t *>(text), static_cast<int>(length));
	PackedInt32Array character_breaks = text_server->string_get_character_breaks(value);
	size_t count = std::min(capacity, static_cast<size_t>(character_breaks.size()));
	for (size_t i = 0; i < count; i++) {
		breaks[i] = static_cast<size_t>(character_breaks[static_cast<int>(i)]);
	}
	return count;
}

static bool _copy_font_bytes(const void *font_data, int length, Vector<uint8_t> &r_bytes) {
	if (font_data == nullptr || length <= 0) {
		r_bytes.clear();
		return false;
	}

	r_bytes.resize(length);
	::memcpy(r_bytes.ptrw(), font_data, length);
	return true;
}

static void _destroy_shared_font_bytes(void *p_data) {
	memdelete(static_cast<Vector<uint8_t> *>(p_data));
}

static bool _register_font_bytes_for_current_thread(const String &family, const Vector<uint8_t> &font_data) {
	// LunaSVG/Plutovg 的字体管理器是线程局部状态，字体字节由共享快照保持存活。
	if (font_data.is_empty()) {
		return false;
	}

	// Godot Vector 使用写时复制；在此保留 Vector，可让线程局部 LunaSVG 字体面拥有
	// 不可变字节，而无需为每个渲染线程完整复制一遍字体。
	Vector<uint8_t> *shared_font_data = memnew(Vector<uint8_t>);
	*shared_font_data = font_data;

	CharString utf8_family = family.utf8();
	const char *family_name = family.is_empty() ? "" : utf8_family.get_data();
	return lunasvg_add_font_face_from_data(family_name, false, false, shared_font_data->ptr(), shared_font_data->size(),
			_destroy_shared_font_bytes, shared_font_data);
}

static void _install_grapheme_break_callback_for_current_thread() {
	lunasvg_set_grapheme_break_func(_get_grapheme_breaks, nullptr);
}

static void _apply_font_preferences_to_current_thread(const FontRegistrySnapshot &snapshot) {
	Vector<CharString> utf8_preferences;
	utf8_preferences.resize(snapshot.font_preferences.size());
	for (int i = 0; i < utf8_preferences.size(); i++) {
		utf8_preferences.set(i, snapshot.font_preferences[i].utf8());
	}

	Vector<const char *> preference_names;
	preference_names.resize(utf8_preferences.size());
	for (int i = 0; i < preference_names.size(); i++) {
		preference_names.set(i, utf8_preferences[i].get_data());
	}
	const char *const empty_preference = nullptr;
	const char *const *preference_data = nullptr;
	if (snapshot.font_preferences_configured) {
		// LunaSVG 用 nullptr 表示项目未提供偏好；显式空偏好则传非空哨兵和零数量，以区分两者。
		preference_data = preference_names.is_empty() ? &empty_preference : preference_names.ptr();
	}
	lunasvg_set_font_preferences(
			preference_data,
			snapshot.font_preferences_configured ? preference_names.size() : 0);
}

static bool _apply_font_registry_snapshot_to_current_thread(const FontRegistrySnapshot &snapshot, uint64_t &r_applied_generation) {
	if (r_applied_generation != snapshot.generation) {
		// reset 开始新字体代；每个渲染线程应用新项目首个快照前先清空本地缓存。
		lunasvg_clear_font_faces();
		r_applied_generation = snapshot.generation;
	}
	if (!snapshot.default_font_data.is_empty() &&
			!_register_font_bytes_for_current_thread("", snapshot.default_font_data)) {
		lunasvg_clear_font_faces();
		return false;
	}
	for (int i = 0; i < snapshot.named_font_faces.size(); i++) {
		if (!_register_font_bytes_for_current_thread(snapshot.named_font_faces[i].family, snapshot.named_font_faces[i].data)) {
			// 字体集不完整时不渲染、也不标记该代已应用；下次加载重试完整不可变快照。
			lunasvg_clear_font_faces();
			return false;
		}
	}
	_apply_font_preferences_to_current_thread(snapshot);
	return true;
}

} // namespace

bool SpxSvgUtils::is_font_data_valid(const Vector<uint8_t> &font_data) {
	if (font_data.is_empty()) {
		return false;
	}

	plutovg_font_face_t *face = plutovg_font_face_load_from_data(font_data.ptr(), font_data.size(), 0, nullptr, nullptr);
	if (face == nullptr) {
		return false;
	}
	plutovg_font_face_destroy(face);

#ifdef LUNASVG_ENABLE_HARFBUZZ
	hb_blob_t *blob = hb_blob_create(reinterpret_cast<const char *>(font_data.ptr()), font_data.size(), HB_MEMORY_MODE_READONLY, nullptr, nullptr);
	hb_face_t *hb_face = hb_face_create(blob, 0);
	const bool harfbuzz_valid = hb_face_get_upem(hb_face) > 0 && hb_face_get_glyph_count(hb_face) > 0;
	hb_face_destroy(hb_face);
	hb_blob_destroy(blob);
	if (!harfbuzz_valid) {
		return false;
	}
#endif
	return true;
}

void SpxSvgUtils::apply_font_registry(const Vector<uint8_t> &default_font_data, const Vector<SpxSvgProjectFontFace> &named_font_faces, const Vector<String> &preferences) {
	// 直接调用方：SpxResMgr 字体事务提交；顶层来自 Go 项目字体初始化。
	// 在锁内一次替换完整快照并递增代号，各渲染线程下次使用时整代安装。
	get_font_registry().replace_all(default_font_data, named_font_faces, preferences);
}

void SpxSvgUtils::set_default_font(const void *font_data, int length) {
	Vector<uint8_t> font_bytes;
	if (!_copy_font_bytes(font_data, length, font_bytes)) {
		return;
	}

	get_font_registry().replace_default_font(font_bytes);
}

void SpxSvgUtils::add_font_face(const String &family, const void *font_data, int length) {
	if (family.is_empty()) {
		return;
	}

	Vector<uint8_t> font_bytes;
	if (!_copy_font_bytes(font_data, length, font_bytes)) {
		return;
	}

	get_font_registry().replace_font_face(family, font_bytes);
}

void SpxSvgUtils::set_font_preferences(const Vector<String> &preferences) {
	get_font_registry().set_font_preferences(preferences);
}

void SpxSvgUtils::reset_font_registry() {
	get_font_registry().reset();
}

bool SpxSvgUtils::ensure_font_faces_registered() {
	// 直接调用方：SVG 栅格化入口；每个渲染线程比较 generation，避免重复注册同一代字体。
	thread_local uint64_t applied_serial = 0; // 当前线程最后成功安装的内容序号。
	thread_local uint64_t applied_generation = 0; // 当前线程已清理并安装的项目代号。
	_install_grapheme_break_callback_for_current_thread();

	FontRegistrySnapshot snapshot;
	if (!get_font_registry().snapshot_if_changed(applied_serial, snapshot)) {
		return true;
	}
	if (_apply_font_registry_snapshot_to_current_thread(snapshot, applied_generation)) {
		applied_serial = snapshot.serial;
		return true;
	}
	return false;
}
