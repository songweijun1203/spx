/**************************************************************************/
/*  spx_sprite_mgr.h                                                      */
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

#ifndef SPX_SPRITE_MGR_H
#define SPX_SPRITE_MGR_H

#include "core/templates/hash_map.h"
#include "gdextension_spx_ext.h"
#include "scene/2d/animated_sprite_2d.h"
#include "spx_manager.h"
#include "spx_layer_sorter.h"
#include <functional>
#include <unordered_set>

typedef std::function<bool(GdColor, GdColor)> ColorCheckFunc;

class SpxSprite;
class ISortableSprite;

// 无向碰撞对：构造时按 ID 排序，使 A-B 与 B-A 在集合中只保留一份。
// 直接使用方是 SpxSpriteMgr 的触发器/像素碰撞状态机；顶层事件来自 Godot Area2D，
// 最终通过 SPX_CALLBACK 回到 Go 的 runtime 触碰事件队列。
class TriggerPair {
public:
	GdObj id1; // 规范化后较小的 Go 精灵对象 ID，不拥有精灵。
	GdObj id2; // 规范化后较大的 Go 精灵对象 ID，不拥有精灵。

public:
	TriggerPair() :
			id1(0), id2(0) {}

	TriggerPair(GdObj p_id1, GdObj p_id2) {
		id1 = p_id1;
		id2 = p_id2;
		if (p_id1 > p_id2) {
			id1 = p_id2;
			id2 = p_id1;
		}
	}
	bool operator<(const TriggerPair &p_other) const {
		return id1 < p_other.id1 || (id1 == p_other.id1 && id2 < p_other.id2);
	}

	bool operator==(const TriggerPair &p_other) const {
		return id1 == p_other.id1 && id2 == p_other.id2;
	}

	bool operator!=(const TriggerPair &p_other) const {
		return !(*this == p_other);
	}

	size_t std_hash() const {
		return static_cast<size_t>(id1) ^ static_cast<size_t>(id2);
	}
};

namespace std {
// 供 unordered_set<TriggerPair> 使用的标准哈希适配，不保存额外状态。
template <>
struct hash<TriggerPair> {
	size_t operator()(const TriggerPair &pair) const {
		return pair.std_hash();
	}
};
} //namespace std

// 精灵系统的 Godot 侧门面：维护 Go 对象 ID 到 SpxSprite 节点的映射，并统一提供
// 创建、视觉、动画、物理、碰撞和批量同步接口。SPX_BIND 接口的直接调用方是生成的
// C/JS-WASM 桥，顶层调用方是 Go runtime_sync、Sprite/Physics/Animation 组件和输入事件。
// 所有 Node 裸指针均由场景树拥有；除纯资源读取外，本类接口应在 Godot 主线程执行。
class SpxSpriteMgr : public SpxManager {
private:
	RBMap<GdObj, SpxSprite *> id_objects; // 活跃 ID 到借用节点指针的索引，不负责 delete。

	std::unordered_set<TriggerPair> bounding_collision_pairs; // Area2D 已重叠、待像素复核的候选对。
	std::unordered_set<TriggerPair> pixel_collision_pairs; // 上一帧确认像素相交的对，用于生成 enter/exit 边沿。

	Node *dont_destroy_root = nullptr; // 引擎场景树中的持久节点根，借用指针。
	Node *sprite_root = nullptr; // 当前场景的精灵挂载根，借用指针。

	// 像素碰撞每隔 N 个世界像素采样一次：1 最精确但最慢，2 通常较均衡，3 以上可能漏掉细小交叠。
	int pixel_collision_sampling_step; // 由 Go 项目物理设置初始化，最小值为 1。

	Ref<Image> _get_current_frame_image(AnimatedSprite2D *sprite);
	Rect2 _get_sprite_aabb(AnimatedSprite2D *anim2d);
	GdBool _check_collision(GdObj obj, ColorCheckFunc check_func);
	GdBool _check_scene_color_collision(GdObj obj, ColorCheckFunc check_func);
	bool _check_pixel_collision_between(SpxSprite *sprite_a, SpxSprite *sprite_b, GdFloat alpha_threshold);
	void _notify_pixel_collision_enter(const TriggerPair &pair);
	void _notify_pixel_collision_exit(const TriggerPair &pair, GdObj skip_id = NULL_OBJECT_ID);
	bool _erase_pixel_collision_pair(const TriggerPair &pair);
	void _remove_collision_pairs_for_sprite(GdObj obj);
	void _check_pixel_collision_events();

protected:
	void _register_sprite(SpxSprite *p_sprite);

public:
	static StringName default_texture_anim; // 单张纹理包装成 SpriteFrames 时使用的固定动画名。
	// 生命周期直接由 SpxEngine 调用；顶层来自 Godot 主循环与 Go 游戏重置/销毁流程。
	void on_awake() override;
	void on_start() override;
	void on_destroy() override;
	void on_update(float delta) override;
	void on_reset(int reset_code) override;

	SpxSprite *get_sprite(GdObj obj);
	void on_sprite_destroy(SpxSprite *sprite);
	void on_trigger_enter(GdInt self_id, GdInt other_id);
	void on_trigger_exit(GdInt self_id, GdInt other_id);
	GdObj _create_sprite(GdString path, GdVec2 pos, GdBool is_backdrop);
	void destroy_all_sprites();
	void collect_sortable_sprites(Vector<ISortableSprite *> &out);

public:
	// 下列 SPX_BIND 方法由接口生成器导出。直接调用方：原生 C ABI 或 Web 桥；
	// 顶层调用方：Go 的 enginewrap.Manager 以及更上层的 Sprite/Game API。
	SPX_BIND void set_dont_destroy_on_load(GdObj obj);
	// Godot 规则：process 与 physics_process 分别在空闲帧和固定物理帧调度。
	SPX_BIND void set_process(GdObj obj, GdBool is_on);
	SPX_BIND void set_physic_process(GdObj obj, GdBool is_on);

	SPX_BIND void set_type_name(GdObj obj, GdString type_name);

	SPX_BIND void set_pivot(GdObj obj, GdVec2 pivot);
	SPX_BIND GdVec2 get_pivot(GdObj obj);

	// 子节点变换接口；NodePath 相对 SpxSprite，返回的节点仍归场景树所有。
	SPX_BIND void set_child_position(GdObj obj, GdString path, GdVec2 pos);
	SPX_BIND GdVec2 get_child_position(GdObj obj, GdString path);
	SPX_BIND void set_child_rotation(GdObj obj, GdString path, GdFloat rot);
	SPX_BIND GdFloat get_child_rotation(GdObj obj, GdString path);
	SPX_BIND void set_child_scale(GdObj obj, GdString path, GdVec2 scale);
	SPX_BIND GdVec2 get_child_scale(GdObj obj, GdString path);

	SPX_BIND GdBool check_collision(GdObj obj, GdObj target, GdBool is_src_trigger, GdBool is_dst_trigger);
	SPX_BIND GdBool check_collision_with_point(GdObj obj, GdVec2 point, GdBool is_click_query);
	SPX_BIND void set_debug_collision_visible(GdObj obj, GdBool visible);
	SPX_BIND GdBool is_debug_collision_visible(GdObj obj);
	SPX_BIND GdObj create_backdrop(GdString path);
	SPX_BIND GdObj create_sprite(GdString path, GdVec2 pos);
	SPX_BIND GdObj create_bare_sprite(GdVec2 pos);
	SPX_BIND GdObj clone_sprite(GdObj obj);
	SPX_BIND GdBool destroy_sprite(GdObj obj);
	SPX_BIND GdBool is_sprite_alive(GdObj obj);
	SPX_BIND void set_position(GdObj obj, GdVec2 pos);
	SPX_BIND void set_transform(GdObj obj, GdVec2 pos, GdFloat rot, GdVec2 scale, GdBool visible, GdVec2 pivot);
	SPX_BIND GdVec2 get_position(GdObj obj);
	SPX_BIND void set_rotation(GdObj obj, GdFloat rot);
	SPX_BIND GdFloat get_rotation(GdObj obj);
	SPX_BIND void set_scale(GdObj obj, GdVec2 scale);
	SPX_BIND GdVec2 get_scale(GdObj obj);
	SPX_BIND void set_render_scale(GdObj obj, GdVec2 scale);
	SPX_BIND GdVec2 get_render_scale(GdObj obj);
	SPX_BIND void set_color(GdObj obj, GdColor color);
	SPX_BIND GdColor get_color(GdObj obj);

	SPX_BIND void set_material_shader(GdObj obj, GdString path);
	SPX_BIND GdString get_material_shader(GdObj obj);
	SPX_BIND void set_material_params(GdObj obj, GdString effect, GdFloat amount);
	SPX_BIND GdFloat get_material_params(GdObj obj, GdString effect);

	SPX_BIND void set_material_params_vec(GdObj obj, GdString effect, GdFloat x, GdFloat y, GdFloat z, GdFloat w);

	SPX_BIND void set_material_params_vec4(GdObj obj, GdString effect, GdVec4 vec4);
	SPX_BIND GdVec4 get_material_params_vec4(GdObj obj, GdString effect);

	SPX_BIND void set_material_params_color(GdObj obj, GdString effect, GdColor color);
	SPX_BIND GdColor get_material_params_color(GdObj obj, GdString effect);

	SPX_BIND void set_texture_atlas(GdObj obj, GdString path, GdRect2 rect2);
	SPX_BIND void set_texture(GdObj obj, GdString path);
	SPX_BIND void set_texture_atlas_direct(GdObj obj, GdString path, GdRect2 rect2);
	SPX_BIND void set_texture_direct(GdObj obj, GdString path);

	SPX_BIND GdString get_texture(GdObj obj);
	SPX_BIND void set_visible(GdObj obj, GdBool visible);
	SPX_BIND GdBool get_visible(GdObj obj);
	SPX_BIND GdInt get_z_index(GdObj obj);
	SPX_BIND void set_z_index(GdObj obj, GdInt z);

	// 动画接口：Manager 只按 ID 路由，具体 Godot AnimatedSprite2D 语义由 SpxSprite 实现。
	SPX_BIND void play_anim(GdObj obj, GdString p_name, GdFloat p_speed, GdBool isLoop, GdBool p_revert);
	SPX_BIND void play_backwards_anim(GdObj obj, GdString p_name);
	SPX_BIND void pause_anim(GdObj obj);
	SPX_BIND void stop_anim(GdObj obj);
	SPX_BIND GdBool is_playing_anim(GdObj obj);
	SPX_BIND void set_anim(GdObj obj, GdString p_name);
	SPX_BIND GdString get_anim(GdObj obj);
	SPX_BIND void set_anim_frame(GdObj obj, GdInt p_frame);
	SPX_BIND GdInt get_anim_frame(GdObj obj);
	SPX_BIND void set_anim_speed_scale(GdObj obj, GdFloat p_speed_scale);
	SPX_BIND GdFloat get_anim_speed_scale(GdObj obj);
	SPX_BIND GdFloat get_anim_playing_speed(GdObj obj);
	SPX_BIND void set_anim_centered(GdObj obj, GdBool p_center);
	SPX_BIND GdBool is_anim_centered(GdObj obj);
	SPX_BIND void set_anim_offset(GdObj obj, GdVec2 p_offset);
	SPX_BIND GdVec2 get_anim_offset(GdObj obj);
	SPX_BIND void set_anim_flip_h(GdObj obj, GdBool p_flip);
	SPX_BIND GdBool is_anim_flipped_h(GdObj obj);
	SPX_BIND void set_anim_flip_v(GdObj obj, GdBool p_flip);
	SPX_BIND GdBool is_anim_flipped_v(GdObj obj);
	SPX_BIND GdString get_current_anim_name(GdObj obj);

	// 物理接口：CharacterBody2D 的接触结果仅在固定物理帧 move_and_slide 后有效。
	SPX_BIND void set_velocity(GdObj obj, GdVec2 velocity);
	SPX_BIND GdVec2 get_velocity(GdObj obj);
	SPX_BIND GdBool is_on_floor(GdObj obj);
	SPX_BIND GdBool is_on_floor_only(GdObj obj);
	SPX_BIND GdBool is_on_wall(GdObj obj);
	SPX_BIND GdBool is_on_wall_only(GdObj obj);
	SPX_BIND GdBool is_on_ceiling(GdObj obj);
	SPX_BIND GdBool is_on_ceiling_only(GdObj obj);
	SPX_BIND GdVec2 get_last_motion(GdObj obj);
	SPX_BIND GdVec2 get_position_delta(GdObj obj);
	SPX_BIND GdVec2 get_floor_normal(GdObj obj);
	SPX_BIND GdVec2 get_wall_normal(GdObj obj);
	SPX_BIND GdVec2 get_real_velocity(GdObj obj);
	SPX_BIND void move_and_slide(GdObj obj);

	SPX_BIND void set_gravity(GdObj obj, GdFloat gravity);
	SPX_BIND GdFloat get_gravity(GdObj obj);
	SPX_BIND void set_mass(GdObj obj, GdFloat mass);
	SPX_BIND GdFloat get_mass(GdObj obj);
	SPX_BIND void add_force(GdObj obj, GdVec2 force);
	// 一次性动量变化：下一动态物理帧增加 impulse / mass，不乘 delta。
	SPX_BIND void add_impulse(GdObj obj, GdVec2 impulse);

	SPX_BIND void set_physics_mode(GdObj obj, GdInt mode);
	SPX_BIND GdInt get_physics_mode(GdObj obj);
	SPX_BIND void set_use_gravity(GdObj obj, GdBool enabled);
	SPX_BIND GdBool is_use_gravity(GdObj obj);
	SPX_BIND void set_gravity_scale(GdObj obj, GdFloat scale);
	SPX_BIND GdFloat get_gravity_scale(GdObj obj);
	SPX_BIND void set_drag(GdObj obj, GdFloat drag);
	SPX_BIND GdFloat get_drag(GdObj obj);
	SPX_BIND void set_friction(GdObj obj, GdFloat friction);
	SPX_BIND GdFloat get_friction(GdObj obj);

	SPX_BIND void set_collision_layer(GdObj obj, GdInt layer);
	SPX_BIND GdInt get_collision_layer(GdObj obj);
	SPX_BIND void set_collision_mask(GdObj obj, GdInt mask);
	SPX_BIND GdInt get_collision_mask(GdObj obj);

	SPX_BIND void set_trigger_layer(GdObj obj, GdInt layer);
	SPX_BIND GdInt get_trigger_layer(GdObj obj);
	SPX_BIND void set_trigger_mask(GdObj obj, GdInt mask);
	SPX_BIND GdInt get_trigger_mask(GdObj obj);

	SPX_BIND void set_collider_rect(GdObj obj, GdVec2 center, GdVec2 size);
	SPX_BIND void set_collider_circle(GdObj obj, GdVec2 center, GdFloat radius);
	SPX_BIND void set_collider_capsule(GdObj obj, GdVec2 center, GdVec2 size);
	SPX_BIND void set_collider_polygon(GdObj obj, GdVec2 center, GdArray points);
	SPX_BIND void set_collision_enabled(GdObj obj, GdBool enabled);
	SPX_BIND GdBool is_collision_enabled(GdObj obj);

	SPX_BIND void set_trigger_rect(GdObj obj, GdVec2 center, GdVec2 size);
	SPX_BIND void set_trigger_circle(GdObj obj, GdVec2 center, GdFloat radius);
	SPX_BIND void set_trigger_capsule(GdObj obj, GdVec2 center, GdVec2 size);
	SPX_BIND void set_trigger_polygon(GdObj obj, GdVec2 center, GdArray points);
	SPX_BIND void set_trigger_enabled(GdObj obj, GdBool trigger);
	SPX_BIND GdBool is_trigger_enabled(GdObj obj);

	// 像素感知：任意不透明源像素与目标场景颜色比较，源像素颜色本身不筛选。
	SPX_BIND GdBool check_collision_by_color(GdObj obj, GdColor color, GdFloat color_threshold, GdFloat alpha_threshold);
	// 双颜色感知：源和目标两侧都使用同一 RGBA 距离阈值。
	SPX_BIND GdBool check_collision_by_colors(GdObj obj, GdColor sprite_color, GdColor target_color, GdFloat color_threshold, GdFloat alpha_threshold);
	SPX_BIND GdBool check_collision_by_alpha(GdObj obj, GdFloat alpha_threshold);
	SPX_BIND GdBool check_collision_with_sprite(GdObj obj, GdObj obj_b, GdFloat alpha_threshold, GdBool use_pixel_perfect);

	// 像素碰撞采样精度配置，由 Go Game 物理设置直接调用。
	SPX_BIND void set_pixel_collision_sampling_step(GdInt step);
	SPX_BIND GdInt get_pixel_collision_sampling_step();

	// 批量同步入口用于减少跨 ABI 次数：transform/retrieve 已由 Go runtime_sync 的帧末
	// flush/pull 使用；visual/physics 对应 Go internal/engine 的序列化缓冲，目前保留为桥接接口。
	// Godot 规则：节点变换和物理状态只能在引擎主线程批量应用。
	SPX_BIND void batch_update_transforms(const float *buffer_data, int len);
	SPX_BIND void batch_update_visuals(const float *buffer_data, int len);
	SPX_BIND GdBool batch_retrieve_positions(const GdObj *objs, int count, SPX_OUT float *out, int out_len);
	SPX_BIND void batch_update_physics(const float *buffer_data, int len);
};

#endif // SPX_SPRITE_MGR_H
