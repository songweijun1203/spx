/**************************************************************************/
/*  spx_pen_mgr.cpp                                                       */
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

#include "spx_pen_mgr.h"

#include "scene/main/viewport.h"

#include "spx_coordinate.h"
#include "spx_engine.h"
#include "spx_object_access.h"
#include "spx_pen_surface.h"
#include "spx_sprite.h"
#include "spx_sprite_mgr.h"

#include <cstdint>
#include <cstring>
#include <type_traits>

#define SPX_WITH_PEN_OR_RETURN(OBJ_ID, BODY)                \
	if (!with_object(OBJ_ID, [&](SpxPen *pen) { BODY; })) { \
		print_error("try to access null SpxPen object");    \
		return;                                             \
	}

namespace {

enum SpxPenBatchCommand {
	SPX_PEN_BATCH_MOVE = 1,
	SPX_PEN_BATCH_DOWN = 2,
	SPX_PEN_BATCH_UP = 3,
	SPX_PEN_BATCH_COLOR = 4,
	SPX_PEN_BATCH_SET_SIZE = 5,
};

constexpr int SPX_PEN_BATCH_FIELDS = 8;

uint32_t read_u32_lane(float p_value) {
	uint32_t bits = 0;
	std::memcpy(&bits, &p_value, sizeof(bits));
	return bits;
}

template <typename T>
T gd_obj_from_i64(int64_t p_value) {
	if constexpr (std::is_pointer_v<T>) {
		return reinterpret_cast<T>(static_cast<uintptr_t>(p_value));
	}
	return static_cast<T>(p_value);
}

GdObj read_gd_obj_lanes(const float *p_record) {
	const uint64_t low = read_u32_lane(p_record[1]);
	const uint64_t high = read_u32_lane(p_record[2]);
	const uint64_t bits = (high << 32) | low;
	int64_t value = 0;
	std::memcpy(&value, &bits, sizeof(value));
	return gd_obj_from_i64<GdObj>(value);
}

bool is_valid_pen_batch_command(float p_value) {
	return p_value == SPX_PEN_BATCH_MOVE ||
			p_value == SPX_PEN_BATCH_DOWN ||
			p_value == SPX_PEN_BATCH_UP ||
			p_value == SPX_PEN_BATCH_COLOR ||
			p_value == SPX_PEN_BATCH_SET_SIZE;
}

} // namespace

// 创建共享画笔 Node/SubViewport 并挂入 SPX 根节点。
// 直接调用方：SpxEngine::on_awake -> SpxManager::on_awake。
// 顶层调用方：Godot SceneTree 主循环 start 阶段。
// Godot 规则：add_child() 后节点由 SceneTree 管理，本 Manager 只保留借用指针。
void SpxPenMgr::on_awake() {
	surface = memnew(SpxPenSurface);
	surface->set_name("pen_root");
	root = surface;
	get_spx_root()->add_child(surface);

	Size2 viewport_size(480, 360);
	if (surface->get_viewport() != nullptr) {
		viewport_size = surface->get_viewport()->get_visible_rect().size;
	}
	surface->initialize(Size2i(MAX(1, (int)Math::ceil(viewport_size.x)), MAX(1, (int)Math::ceil(viewport_size.y))));
}

void SpxPenMgr::on_update(float delta) {
	_update_all(delta);
}

// 直接调用方：SpxEngine::on_destroy；顶层调用方：Godot 主循环退出或模块反初始化。
void SpxPenMgr::on_destroy() {
	surface = nullptr;
	_destroy_objects_and_root();
}

// 直接调用方：SpxEngine::_do_reset；顶层调用方：Go RequestReset/Web 下一局重启流程。
void SpxPenMgr::on_reset(int reset_code) {
	_reset_objects(reset_code);
	if (surface != nullptr) {
		surface->clear();
	}
}

GdObj SpxPenMgr::create_pen() {
	return _create_object();
}

void SpxPenMgr::destroy_pen(GdObj obj) {
	destroy_object(obj);
}

// 解码 Go PenSyncBuffer 的定长 float32 命令包，一次跨语言调用保持原始命令顺序。
// 直接调用方：spx_pen_batch_update_commands/gdspx_pen_batch_update_commands ABI 包装。
// 顶层调用方：Go 每帧画笔同步或立即操作前的 PenSyncBuffer.Barrier。
// 所有记录先整体校验再执行，避免畸形 ABI 输入造成半批次状态更新。
void SpxPenMgr::batch_update_commands(const float *buffer_data, int len) {
	if (unlikely(!_require_main_thread(__func__))) {
		return;
	}

	// 缓冲布局：[count] + count x [op, idLowBits, idHighBits, a, b, c, d, reserved]。
	if (buffer_data == nullptr || len < 1) {
		return;
	}

	const float count_value = buffer_data[0];
	const int max_count_for_length = (len - 1) / SPX_PEN_BATCH_FIELDS;
	if (!Math::is_finite(count_value) || count_value < 0.0f || Math::floor(count_value) != count_value || count_value > max_count_for_length) {
		print_error("batch_update_commands: command count is invalid.");
		return;
	}

	const int command_count = (int)count_value;
	const int64_t required_length = 1 + (int64_t)command_count * SPX_PEN_BATCH_FIELDS;
	if (required_length != len) {
		print_error("batch_update_commands: buffer length is invalid.");
		return;
	}

	for (int i = 0; i < command_count; i++) {
		const float *record = &buffer_data[1 + i * SPX_PEN_BATCH_FIELDS];
		if (!is_valid_pen_batch_command(record[0])) {
			print_error("batch_update_commands: command type is invalid.");
			return;
		}
	}

	bool has_missing_pen = false;
	for (int i = 0; i < command_count; i++) {
		const float *record = &buffer_data[1 + i * SPX_PEN_BATCH_FIELDS];
		SpxPen *pen = _find_object(read_gd_obj_lanes(record));
		if (pen == nullptr) {
			has_missing_pen = true;
			continue;
		}

		const int command = (int)record[0];
		switch (command) {
			case SPX_PEN_BATCH_MOVE:
				pen->move_to(spx_to_godot_vec2(GdVec2(record[3], record[4])));
				break;
			case SPX_PEN_BATCH_DOWN:
				pen->on_down(record[3] != 0.0f);
				break;
			case SPX_PEN_BATCH_UP:
				pen->on_up();
				break;
			case SPX_PEN_BATCH_COLOR:
				pen->set_color_to(GdColor(record[3], record[4], record[5], record[6]));
				break;
			case SPX_PEN_BATCH_SET_SIZE:
				pen->set_size_to(record[3]);
				break;
		}
	}

	if (has_missing_pen) {
		print_error("batch_update_commands: one or more pen objects do not exist.");
	}
}

void SpxPenMgr::destroy_all_pens() {
	if (unlikely(!_require_main_thread(__func__))) {
		return;
	}

	if (surface != nullptr) {
		surface->clear();
	}
	for (const auto &[id, pen] : id_objects) {
		pen->on_erase_all();
	}
}

void SpxPenMgr::set_canvas_size(GdInt width, GdInt height) {
	if (unlikely(!_require_main_thread(__func__))) {
		return;
	}

	if (surface != nullptr) {
		surface->set_canvas_size(Size2i(width, height));
	}
}

// 将本帧累计命令提交给离屏画布。
// 直接调用方：SpxEngine::on_update；顶层调用方：Godot 每个普通渲染/逻辑帧。
void SpxPenMgr::flush_all() {
	if (unlikely(!_require_main_thread(__func__))) {
		return;
	}

	if (surface != nullptr) {
		surface->flush();
	}
}

void SpxPenMgr::move_pen_to(GdObj obj, GdVec2 position) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->move_to(spx_to_godot_vec2(position)))
}

void SpxPenMgr::pen_stamp(GdObj obj) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->stamp())
}

void SpxPenMgr::pen_stamp_sprite(GdObj sprite_id) {
	SpxSprite *sprite = spx_checked_lookup<SpxSprite>(sprite_id, __func__, spriteMgr,
			[](SpxSpriteMgr *mgr, GdObj id) { return mgr->get_sprite(id); });
	if (sprite == nullptr || surface == nullptr) {
		return;
	}
	surface->draw_stamp(sprite->get_anim2d());
}

// 为像素感知提供画笔层快照。
// 直接调用方：SpxSpriteMgr 的颜色/透明度查询；顶层调用方：Go TouchingColor 等 API。
bool SpxPenMgr::capture(const Rect2 &p_query_bounds, SpxPixelQuery::Snapshot &r_snapshot) {
	return surface != nullptr && surface->capture(p_query_bounds, r_snapshot);
}

void SpxPenMgr::pen_down(GdObj obj, GdBool move_by_mouse) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->on_down(move_by_mouse))
}

void SpxPenMgr::pen_up(GdObj obj) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->on_up())
}

void SpxPenMgr::set_pen_color_to(GdObj obj, GdColor color) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->set_color_to(color))
}

void SpxPenMgr::change_pen_by(GdObj obj, GdInt property, GdFloat amount) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->change_by(property, amount))
}

void SpxPenMgr::set_pen_to(GdObj obj, GdInt property, GdFloat value) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->set_to(property, value))
}

void SpxPenMgr::change_pen_size_by(GdObj obj, GdFloat amount) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->change_size_by(amount))
}

void SpxPenMgr::set_pen_size_to(GdObj obj, GdFloat size) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->set_size_to(size))
}

void SpxPenMgr::set_pen_stamp_texture(GdObj obj, GdString texture_path) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->set_stamp_texture(texture_path))
}

void SpxPenMgr::pen_stamp_with_transform(GdObj obj, GdString texture_path, GdVec2 position, GdFloat rotation_radians, GdVec2 scale) {
	SPX_WITH_PEN_OR_RETURN(obj, pen->stamp_with_transform(texture_path, spx_to_godot_vec2(position), rotation_radians, scale))
}
