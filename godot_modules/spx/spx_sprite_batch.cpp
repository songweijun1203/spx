/**************************************************************************/
/*  spx_sprite_batch.cpp                                                    */
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

#include "spx_sprite_mgr.h"

#include "spx_batch_validation.h"
#include "spx_coordinate.h"
#include "spx_sprite.h"

#include <climits>
#include <cstring>
#include <limits>
#include <type_traits>

namespace {

// 与 Go internal/engine/physics_sync.go 保持一致的跨语言命令码；修改时必须同步两端协议。
enum SpxPhysicsBatchCmd {
	SPX_PHYSICS_CMD_VELOCITY = 1,
	SPX_PHYSICS_CMD_GRAVITY = 2,
	SPX_PHYSICS_CMD_MASS = 3,
	SPX_PHYSICS_CMD_MODE = 4,
	SPX_PHYSICS_CMD_USE_GRAVITY = 5,
	SPX_PHYSICS_CMD_GRAVITY_SCALE = 6,
	SPX_PHYSICS_CMD_DRAG = 7,
	SPX_PHYSICS_CMD_FRICTION = 8,
	SPX_PHYSICS_CMD_COLLISION_LAYER = 9,
	SPX_PHYSICS_CMD_COLLISION_MASK = 10,
	SPX_PHYSICS_CMD_TRIGGER_LAYER = 11,
	SPX_PHYSICS_CMD_TRIGGER_MASK = 12,
	SPX_PHYSICS_CMD_COLLISION_ENABLED = 13,
	SPX_PHYSICS_CMD_TRIGGER_ENABLED = 14,
};

static constexpr int SPX_PHYSICS_BATCH_FIELDS = 6; // 每条物理命令固定占用的 float32 lane 数。

uint32_t read_u32_lane(float value) {
	uint32_t bits = 0;
	std::memcpy(&bits, &value, sizeof(bits));
	return bits;
}

template <typename T>
T gd_obj_from_i64(int64_t value) {
	if constexpr (std::is_pointer_v<T>) {
		return reinterpret_cast<T>(static_cast<uintptr_t>(value));
	}
	return static_cast<T>(value);
}

GdObj read_gd_obj_lanes(const float *record) {
	uint64_t low = read_u32_lane(record[1]);
	uint64_t high = read_u32_lane(record[2]);
	uint64_t bits = (high << 32) | low;
	int64_t value = 0;
	std::memcpy(&value, &bits, sizeof(value));
	return gd_obj_from_i64<GdObj>(value);
}

bool decode_batch_count(float value, int &result, const char *op_name, const char *field_name) {
	if (SpxBatchValidation::decode_nonnegative_count(value, result)) {
		return true;
	}
	print_error(String(op_name) + ": " + field_name + " must be a finite, non-negative integer within int range.");
	return false;
}

bool decode_batch_int(float value, int &result, const char *op_name, const char *field_name) {
	if (SpxBatchValidation::decode_int(value, result)) {
		return true;
	}
	print_error(String(op_name) + ": " + field_name + " must be a finite integer within int range.");
	return false;
}

bool decode_legacy_gd_obj(float value, GdObj &result, const char *op_name) {
	const double numeric_value = static_cast<double>(value);
	// 旧版批处理用整数值 float 承载 GdObj；上界采用开区间，避免转换 2^63 时溢出 int64_t。
	constexpr double gd_obj_upper_bound = 9223372036854775808.0;
	if (!std::isfinite(numeric_value) || std::trunc(numeric_value) != numeric_value ||
			numeric_value < 0.0 || numeric_value >= gd_obj_upper_bound) {
		print_error(String(op_name) + ": sprite ID must be a finite, non-negative integer within GdObj range.");
		return false;
	}

	result = static_cast<GdObj>(static_cast<int64_t>(numeric_value));
	return true;
}

GdObj legacy_gd_obj_from_float(float value) {
	return static_cast<GdObj>(static_cast<int64_t>(value));
}

bool validate_batch_length(int length, int header, int count, int stride, const char *operation, int trailing = 0) {
	const int64_t expected = int64_t(header) + int64_t(count) * stride + trailing;
	if (expected > INT_MAX || length != expected) {
		print_error(String(operation) + ": buffer length does not match the record counts.");
		return false;
	}
	return true;
}

} // namespace

void SpxSpriteMgr::batch_update_transforms(const float *buffer_data, int len) {
	// 直接调用方：ABI 的 spx_sprite_mgr_batch_update_transforms；顶层调用方：Go
	// runtime_sync.flushSyncBuffer。Godot Node 不是线程安全对象，故强制主线程执行。
	ERR_FAIL_COND_MSG(!Thread::is_main_thread(), "SPX transform batches may only be applied on the engine main thread.");
	const char *op_name = "batch_update_transforms";
	// 缓冲格式：[更新数, 删除数, 更新记录..., 删除 ID...]；每条更新记录 9 个 float lane：
	// [id, x, y, rotation, scaleX, scaleY, renderOffsetX, renderOffsetY, visible]。
	const int FIELDS_PER_SPRITE = 9;
	const int HEADER_SIZE = 2;

	if (buffer_data == nullptr) {
		return;
	}

	if (len < HEADER_SIZE) {
		return;
	}

	int update_count = 0;
	int delete_count = 0;
	if (!decode_batch_count(buffer_data[0], update_count, op_name, "update count") ||
			!decode_batch_count(buffer_data[1], delete_count, op_name, "delete count")) {
		return;
	}

	if (!validate_batch_length(len, HEADER_SIZE, update_count, FIELDS_PER_SPRITE, op_name, delete_count)) {
		return;
	}

	// 修改节点前先校验所有记录，避免畸形数据包留下只应用一部分的帧状态。
	int idx = HEADER_SIZE;
	for (int i = 0; i < update_count; i++) {
		GdObj sprite_id = 0;
		if (!decode_legacy_gd_obj(buffer_data[idx], sprite_id, op_name)) {
			return;
		}
		if (!SpxBatchValidation::all_finite(&buffer_data[idx + 1], FIELDS_PER_SPRITE - 1)) {
			print_error(String(op_name) + ": transform values must be finite.");
			return;
		}
		idx += FIELDS_PER_SPRITE;
	}

	const int delete_start = idx;
	for (int i = 0; i < delete_count; i++) {
		GdObj sprite_id = 0;
		if (!decode_legacy_gd_obj(buffer_data[idx++], sprite_id, op_name)) {
			return;
		}
	}

	// 同一帧内删除优先：先排队全部删除再应用更新；get_sprite 将待删节点视为墓碑，
	// 后续批次也不会再命中这些节点。
	idx = delete_start;
	for (int i = 0; i < delete_count; i++) {
		const GdObj sprite_id = legacy_gd_obj_from_float(buffer_data[idx++]);
		if (get_sprite(sprite_id) != nullptr) {
			destroy_sprite(sprite_id);
		}
	}

	idx = HEADER_SIZE;
	for (int i = 0; i < update_count; i++) {
		const GdObj sprite_id = legacy_gd_obj_from_float(buffer_data[idx]);
		auto x = buffer_data[idx + 1];
		auto y = buffer_data[idx + 2];
		auto rotation = buffer_data[idx + 3];
		auto scale_x = buffer_data[idx + 4];
		auto scale_y = buffer_data[idx + 5];
		auto render_offset_x = buffer_data[idx + 6];
		auto render_offset_y = buffer_data[idx + 7];
		auto visible = buffer_data[idx + 8] != 0.0;

		idx += FIELDS_PER_SPRITE;

		SpxSprite *sprite = get_sprite(sprite_id);
		if (sprite == nullptr) {
			continue;
		}

		sprite->set_position(spx_to_godot_vec2(GdVec2(x, y)));
		sprite->set_rotation(rotation);
		sprite->set_scale(GdVec2(scale_x, scale_y));
		sprite->set_visible(visible);
		sprite->on_set_visible(visible);
		sprite->set_render_offset(spx_to_godot_vec2(GdVec2(render_offset_x, render_offset_y)));
	}
}

void SpxSpriteMgr::batch_update_visuals(const float *buffer_data, int len) {
	// 直接调用方：ABI；Go 侧对应 internal/engine.SyncBatchUpdateVisuals 与 VisualSyncBuffer。
	// 当前生产运行时的 applyCostumeUpdate 仍逐项调用，故此入口主要由桥接接口与测试覆盖。
	// 一批内先完成格式校验再改节点，避免畸形跨语言数据造成部分提交。
	ERR_FAIL_COND_MSG(!Thread::is_main_thread(), "SPX visual batches may only be applied on the engine main thread.");
	const char *op_name = "batch_update_visuals";
	// 缓冲格式：[count, entry0..., entry1..., ...]；每条记录 9 个 float：
	// [spriteId, renderScaleX, renderScaleY, zIndex, flags, uvX, uvY, uvW, uvH]。
	const int VISUAL_FIELDS_PER_SPRITE = 9;
	const int HEADER_SIZE = 1;
	const int FLAG_HAS_ZINDEX = 1;
	const int FLAG_HAS_UV_REMAP = 2;

	if (buffer_data == nullptr) {
		return;
	}

	if (len < HEADER_SIZE) {
		return;
	}

	int count = 0;
	if (!decode_batch_count(buffer_data[0], count, op_name, "record count")) {
		return;
	}

	if (!validate_batch_length(len, HEADER_SIZE, count, VISUAL_FIELDS_PER_SPRITE, op_name)) {
		return;
	}

	// 应用任何视觉状态前先校验完整数据包。
	int idx = HEADER_SIZE;
	for (int i = 0; i < count; i++) {
		GdObj sprite_id = 0;
		int flags = 0;
		if (!decode_legacy_gd_obj(buffer_data[idx], sprite_id, op_name)) {
			return;
		}
		if (!SpxBatchValidation::all_finite(&buffer_data[idx + 1], 2)) {
			print_error(String(op_name) + ": render scale must be finite.");
			return;
		}
		if (!decode_batch_int(buffer_data[idx + 4], flags, op_name, "visual flags")) {
			return;
		}
		if ((flags & ~(FLAG_HAS_ZINDEX | FLAG_HAS_UV_REMAP)) != 0) {
			print_error(String(op_name) + ": visual flags contain unknown bits.");
			return;
		}
		if (flags & FLAG_HAS_ZINDEX) {
			int z_index = 0;
			if (!decode_batch_int(buffer_data[idx + 3], z_index, op_name, "z-index")) {
				return;
			}
			if (z_index < RS::CANVAS_ITEM_Z_MIN || z_index > RS::CANVAS_ITEM_Z_MAX) {
				print_error(String(op_name) + ": z-index is outside the CanvasItem range.");
				return;
			}
		}
		if ((flags & FLAG_HAS_UV_REMAP) && !SpxBatchValidation::all_finite(&buffer_data[idx + 5], 4)) {
			print_error(String(op_name) + ": UV remap values must be finite.");
			return;
		}
		idx += VISUAL_FIELDS_PER_SPRITE;
	}

	idx = HEADER_SIZE;
	for (int i = 0; i < count; i++) {
		const int record_idx = idx;
		const GdObj sprite_id = legacy_gd_obj_from_float(buffer_data[record_idx]);
		const float render_scale_x = buffer_data[record_idx + 1];
		const float render_scale_y = buffer_data[record_idx + 2];
		const int flags = static_cast<int>(buffer_data[record_idx + 4]);
		const float uv_x = buffer_data[record_idx + 5];
		const float uv_y = buffer_data[record_idx + 6];
		const float uv_w = buffer_data[record_idx + 7];
		const float uv_h = buffer_data[record_idx + 8];

		idx += VISUAL_FIELDS_PER_SPRITE;

		SpxSprite *sprite = get_sprite(sprite_id);
		if (sprite == nullptr) {
			continue;
		}

		sprite->set_render_scale(GdVec2(render_scale_x, render_scale_y));

		if (flags & FLAG_HAS_ZINDEX) {
			sprite->set_z_index(static_cast<int>(buffer_data[record_idx + 3]));
		}

		if (flags & FLAG_HAS_UV_REMAP) {
			sprite->set_uv_remap(GdVec4(uv_x, uv_y, uv_w, uv_h));
		}
	}
}

GdBool SpxSpriteMgr::batch_retrieve_positions(const GdObj *objs, int count, float *out, int out_len) {
	// 直接调用方：ABI；顶层调用方：Go runtime_sync.pullPhysicsPositions，
	// 在物理步完成后把 Godot CharacterBody2D 的权威位置拉回 Go 状态。
	ERR_FAIL_COND_V_MSG(!Thread::is_main_thread(), false, "SPX sprite positions may only be read on the engine main thread.");
	if (count < 0 || count > INT_MAX / 2) {
		print_error("batch_retrieve_positions: invalid count.");
		return false;
	}
	if (out_len != count * 2 || (count > 0 && (!objs || !out))) {
		print_error("batch_retrieve_positions: invalid buffers or output length.");
		return false;
	}
	for (int i = 0; i < count; ++i) {
		SpxSprite *sprite = get_sprite(objs[i]);
		if (sprite != nullptr) {
			const Vector2 pos = godot_to_spx_vec2(sprite->get_position());
			out[i * 2] = pos.x;
			out[i * 2 + 1] = pos.y;
		} else {
			const float missing = std::numeric_limits<float>::quiet_NaN();
			out[i * 2] = missing;
			out[i * 2 + 1] = missing;
		}
	}
	return true;
}

void SpxSpriteMgr::batch_update_physics(const float *buffer_data, int len) {
	// 直接调用方：ABI；Go 侧对应 internal/engine.PhysicsSyncBuffer，当前尚无生产 flush 入口。
	// 形状、层掩码和模式均涉及
	// PhysicsServer/场景树对象，只能在 Godot 主线程、固定物理步之外安全配置。
	// 缓冲格式：[count] + count x [cmd, spriteIdLowBits, spriteIdHighBits, a, b, reserved0]。
	// 整数 lane 使用 float32 原始位承载，确保 32/64 位 ID 和掩码不丢精度。
	ERR_FAIL_COND_MSG(!Thread::is_main_thread(), "SPX physics batches may only be applied on the engine main thread.");
	if (buffer_data == nullptr || len < 1) {
		return;
	}

	int count = 0;
	if (!decode_batch_count(buffer_data[0], count, "batch_update_physics", "record count")) {
		return;
	}
	if (!validate_batch_length(len, 1, count, SPX_PHYSICS_BATCH_FIELDS, "batch_update_physics")) {
		return;
	}

	// 执行命令前校验全部操作码及数值 lane；ID、mode、layer 和 mask 刻意保持为 float32 原始位。
	int idx = 1;
	for (int i = 0; i < count; i++) {
		int cmd = 0;
		if (!decode_batch_int(buffer_data[idx], cmd, "batch_update_physics", "command")) {
			return;
		}
		const float *args = &buffer_data[idx + 3];
		switch (cmd) {
			case SPX_PHYSICS_CMD_VELOCITY:
				if (!SpxBatchValidation::all_finite(args, 2)) {
					print_error("batch_update_physics: velocity values must be finite.");
					return;
				}
				break;
			case SPX_PHYSICS_CMD_GRAVITY:
			case SPX_PHYSICS_CMD_MASS:
			case SPX_PHYSICS_CMD_USE_GRAVITY:
			case SPX_PHYSICS_CMD_GRAVITY_SCALE:
			case SPX_PHYSICS_CMD_DRAG:
			case SPX_PHYSICS_CMD_FRICTION:
			case SPX_PHYSICS_CMD_COLLISION_ENABLED:
			case SPX_PHYSICS_CMD_TRIGGER_ENABLED:
				if (!SpxBatchValidation::all_finite(args, 1)) {
					print_error("batch_update_physics: numeric command value must be finite.");
					return;
				}
				break;
			case SPX_PHYSICS_CMD_MODE:
			case SPX_PHYSICS_CMD_COLLISION_LAYER:
			case SPX_PHYSICS_CMD_COLLISION_MASK:
			case SPX_PHYSICS_CMD_TRIGGER_LAYER:
			case SPX_PHYSICS_CMD_TRIGGER_MASK:
				break;
			default:
				print_error("batch_update_physics: unknown command " + itos(cmd) + ".");
				return;
		}
		idx += SPX_PHYSICS_BATCH_FIELDS;
	}

	idx = 1;
	for (int i = 0; i < count; i++) {
		const int cmd = static_cast<int>(buffer_data[idx]);
		GdObj obj = read_gd_obj_lanes(&buffer_data[idx]);
		SpxSprite *sprite = get_sprite(obj);
		if (sprite != nullptr) {
			float a = buffer_data[idx + 3];
			float b = buffer_data[idx + 4];
			switch (cmd) {
				case SPX_PHYSICS_CMD_VELOCITY:
					sprite->set_velocity(spx_to_godot_vec2(GdVec2(a, b)));
					break;
				case SPX_PHYSICS_CMD_GRAVITY:
					sprite->set_gravity(a);
					break;
				case SPX_PHYSICS_CMD_MASS:
					sprite->set_mass(a);
					break;
				case SPX_PHYSICS_CMD_MODE:
					sprite->set_physics_mode((GdInt)(int32_t)read_u32_lane(a));
					break;
				case SPX_PHYSICS_CMD_USE_GRAVITY:
					sprite->set_use_gravity(a != 0);
					break;
				case SPX_PHYSICS_CMD_GRAVITY_SCALE:
					sprite->set_gravity_scale(a);
					break;
				case SPX_PHYSICS_CMD_DRAG:
					sprite->set_drag(a);
					break;
				case SPX_PHYSICS_CMD_FRICTION:
					sprite->set_friction(a);
					break;
				case SPX_PHYSICS_CMD_COLLISION_LAYER:
					sprite->set_collision_layer((GdInt)read_u32_lane(a));
					break;
				case SPX_PHYSICS_CMD_COLLISION_MASK:
					sprite->set_collision_mask((GdInt)read_u32_lane(a));
					break;
				case SPX_PHYSICS_CMD_TRIGGER_LAYER:
					sprite->set_trigger_layer((GdInt)read_u32_lane(a));
					break;
				case SPX_PHYSICS_CMD_TRIGGER_MASK:
					sprite->set_trigger_mask((GdInt)read_u32_lane(a));
					break;
				case SPX_PHYSICS_CMD_COLLISION_ENABLED:
					sprite->set_collision_enabled(a != 0);
					break;
				case SPX_PHYSICS_CMD_TRIGGER_ENABLED:
					sprite->set_trigger_enabled(a != 0);
					break;
				default:
					break;
			}
		}
		idx += SPX_PHYSICS_BATCH_FIELDS;
	}
}

void SpxSpriteMgr::set_pixel_collision_sampling_step(GdInt step) {
	// 限制到有效范围；最小为 1，零或负数会使采样循环无法推进。
	if (step < 1) {
		pixel_collision_sampling_step = 1;
		print_error("pixel_collision_sampling_step must be at least 1. Setting to 1.");
	} else {
		pixel_collision_sampling_step = step;
	}
}

GdInt SpxSpriteMgr::get_pixel_collision_sampling_step() {
	return pixel_collision_sampling_step;
}
