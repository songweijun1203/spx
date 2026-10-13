/**************************************************************************/
/*  spx_audio_bus_pool.h                                                       */
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

#ifndef SPX_AUDIO_BUS_POOL_H
#define SPX_AUDIO_BUS_POOL_H

#include "core/string/string_name.h"
#include "core/templates/hash_set.h"
#include "core/templates/vector.h"
#include "gdextension_spx_ext.h"

// AudioServer 总线池。为需要独立音量/声像的 SpxAudio 租用可复用总线，
// 避免每次播放反复创建 Godot Audio Bus 和 AudioEffectPanner。
// 直接调用方：SpxAudio/SpxAudioMgr；顶层调用方：Go AudioMgr 的音量、声像和生命周期 API。
// AudioServer 是引擎全局对象，本类及全部容器只允许 Godot 主线程访问。
class SpxAudioBusPool {
	// 进程内唯一实例；由 SpxAudioMgr::on_awake 创建、on_destroy 销毁。
	static inline SpxAudioBusPool *singleton = nullptr;

public:
	// Godot 规定 Master 总线固定为索引 0；项目总线仍按名称查找，不能缓存索引。
	static const int BUS_MASTER = 0;
	// Godot 内置主总线名。
	static StringName STR_BUS_MASTER;
	// SPX 共用音效总线名；如项目已有同名总线则复用但不取得所有权。
	static StringName STR_BUS_SFX;
	// SPX 共用音乐总线名；如项目已有同名总线则复用但不取得所有权。
	static StringName STR_BUS_MUSIC;

private:
	// 初始化时至少准备的可租用专用总线数。
	static const int DEFAULT_POOLED_BUS_COUNT = 1;
	// 池耗尽时一次新增的总线数量，降低频繁修改 AudioServer 布局的成本。
	static const int BUS_EXPANSION_SIZE = 4;

	// 当前可租用的总线名；池拥有名称集合，实际总线对象由 AudioServer 持有。
	Vector<StringName> free_buses;
	// 已租出的专用总线，用于校验重复归还和非法访问。
	HashSet<StringName> active_buses;
	// 所有池化专用总线名（空闲和已租出），用于 reset 后重建空闲表。
	HashSet<StringName> pooled_buses;
	// 本池实际创建的总线名；shutdown 仅删除这些，绝不删除项目自带总线。
	HashSet<StringName> created_buses;

private:
	int ensure_bus(const StringName &p_name);
	void expand_buses(int p_count = BUS_EXPANSION_SIZE);
	void reset_bus(int p_id);
	int get_valid_bus_id(const StringName &p_name) const;

public:
	// 直接调用方：SpxAudioMgr 生命周期；顶层来源：SpxEngine 启动、reset、destroy。
	static SpxAudioBusPool *get_singleton();
	static void init();
	static void reset();
	static void shutdown();
	// 直接调用方：SpxAudio::ensure_dedicated_bus；顶层为 Go 设置音量/声像。
	StringName alloc();
	void free(const StringName &p_name);
	void set_volume(const StringName &p_name, GdFloat p_volume);
	GdFloat get_volume(const StringName &p_name);
	void set_pan(const StringName &p_name, GdFloat p_pan);
	GdFloat get_pan(const StringName &p_name);
};
#endif // SPX_AUDIO_BUS_POOL_H
