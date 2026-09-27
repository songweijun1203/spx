/**************************************************************************/
/*  spx_physics_mgr.h                                                     */
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

#ifndef SPX_PHYSICS_MGR_H
#define SPX_PHYSICS_MGR_H

#include "gdextension_spx_ext.h"
#include "spx_manager.h"

class PhysicsDirectSpaceState2D;

// SPX 物理计算使用的全局倍率配置。SpxSprite 的物理步进读取这些静态值，
// SpxPhysicsMgr 仅提供 ABI 门面；其生命周期与单次游戏配置一致。
class SpxPhysicsDefine {
private:
	// 重力倍率，由 Go 项目物理配置写入；不拥有 Godot 资源。
	static inline GdFloat global_gravity = 1.0;
	// 地面摩擦倍率，由精灵物理更新读取。
	static inline GdFloat global_friction = 1.0;
	// 空气阻力倍率，由精灵物理更新读取。
	static inline GdFloat global_air_drag = 1.0;

public:
	static void set_global_gravity(GdFloat gravity);
	static GdFloat get_global_gravity();
	static void set_global_friction(GdFloat friction);
	static GdFloat get_global_friction();
	static void set_global_air_drag(GdFloat air_drag);
	static GdFloat get_global_air_drag();
};

enum class ColliderType {
	NONE = 0,
	AUTO = 1,
	CIRCLE = 2,
	RECT = 3,
	CAPSULE = 4,
	POLYGON = 5
};

enum BoundaryType {
	BOUND_LEFT = 1 << 0,
	BOUND_TOP = 1 << 1,
	BOUND_RIGHT = 1 << 2,
	BOUND_BOTTOM = 1 << 3
};

// SPX 物理查询门面，把 Go 的射线、形状重叠和舞台/相机边缘检测映射到
// Godot PhysicsDirectSpaceState2D，并负责 SPX/Godot 坐标转换和 ABI 结果编码。
// 直接调用方：生成的 spx_physics_* ABI；内部依赖 SpriteMgr 与 CameraMgr；
// 顶层调用方：Go PhysicsMgr、Sprite Touching/边缘检测、射线与范围查询 API。
// Godot 规则：direct_space_state 和场景节点只可在主线程、有效 World2D 内访问；
// 查询应在物理世界可读阶段执行，不能跨线程长期保存返回的 collider 裸指针。
class SpxPhysicsMgr : public SpxManager {
private:
	// 射线查询的 ABI 中间结果；坐标和法线保持 SPX 坐标系。
	struct RayHit {
		// 是否命中任意 Area2D/PhysicsBody2D。
		GdBool collide = false;
		// 命中 SpxSprite 时的逻辑 ID；非 SPX 碰撞体为 0。
		GdObj sprite_id = 0;
		// SPX 世界坐标中的命中点。
		GdVec2 position;
		// SPX 世界坐标中的表面法线。
		GdVec2 normal;
		// 转为 Go ABI 使用的定长 int64 数组，浮点值按 1e4 定点编码。
		GdArray to_array() const;
	};

	PhysicsDirectSpaceState2D *_get_space_state();
	GdArray _query_shape(RID shape, GdVec2 pos, GdInt collision_mask);
	RayHit _query_ray(GdVec2 from, GdVec2 to, GdArray ignore_sprites, GdInt collision_mask, GdBool collide_with_areas, GdBool collide_with_bodies);

	// 相机/舞台边缘检测的内部共用实现。
	GdInt _check_touched_boundaries(GdObj obj, GdBool use_stage_limits);
	GdBool _check_touched_boundary(GdObj obj, GdInt board_type, GdBool use_stage_limits);
	GdInt _check_nearest_touched_boundary(GdObj obj, GdBool use_stage_limits);

public:
	// 当前触碰判定策略：true 为像素透明度，false 为 Godot 形状碰撞。
	// 仅主线程读写，SpxSprite 的感知逻辑会读取该开关。
	bool is_collision_by_pixel;
	// 直接调用方：SpxEngine::_notify_managers；顶层来源：Godot 启动。
	void on_awake() override;

public:
	// 以下 SPX_BIND 由 ABI 直接调用，顶层均来自 Go PhysicsMgr/精灵感知 API。
	SPX_BIND GdObj raycast(GdVec2 from, GdVec2 to, GdInt collision_mask);
	SPX_BIND GdBool check_collision(GdVec2 from, GdVec2 to, GdInt collision_mask, GdBool collide_with_areas, GdBool collide_with_bodies);
	SPX_BIND GdInt check_touched_camera_boundaries(GdObj obj);
	SPX_BIND GdBool check_touched_camera_boundary(GdObj obj, GdInt board_type);
	SPX_BIND GdInt check_nearest_touched_camera_boundary(GdObj obj);

	// 舞台限制边缘检测接口。
	SPX_BIND GdInt check_touched_stage_boundaries(GdObj obj);
	SPX_BIND GdBool check_touched_stage_boundary(GdObj obj, GdInt board_type);
	SPX_BIND GdInt check_nearest_touched_stage_boundary(GdObj obj);
	SPX_BIND void set_collision_system_type(GdBool is_collision_by_alpha);

	// SPX 全局物理倍率配置。
	SPX_BIND void set_global_gravity(GdFloat gravity);
	SPX_BIND GdFloat get_global_gravity();
	SPX_BIND void set_global_friction(GdFloat friction);
	SPX_BIND GdFloat get_global_friction();
	SPX_BIND void set_global_air_drag(GdFloat air_drag);
	SPX_BIND GdFloat get_global_air_drag();

	// 临时形状重叠和带详情射线查询。
	SPX_BIND GdArray check_collision_rect(GdVec2 pos, GdVec2 size, GdInt collision_mask);
	SPX_BIND GdArray check_collision_circle(GdVec2 pos, GdFloat radius, GdInt collision_mask);
	SPX_BIND GdArray raycast_with_details(GdVec2 from, GdVec2 to, GdArray ignore_sprites, GdInt collision_mask, GdBool collide_with_areas, GdBool collide_with_bodies);
};

#endif // SPX_PHYSICS_MGR_H
