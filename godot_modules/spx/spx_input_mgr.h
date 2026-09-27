/**************************************************************************/
/*  spx_input_mgr.h                                                       */
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

#ifndef SPX_INPUT_MGR_H
#define SPX_INPUT_MGR_H

#include "core/string/string_name.h"
#include "core/templates/hash_map.h"
#include "core/templates/vector.h"
#include "gdextension_spx_ext.h"
#include "spx_manager.h"
#include "spx_input_proxy.h"

// SPX 输入查询服务。轮询 Godot Input 单例，并创建 SpxInputProxy 将离散键鼠事件
// 回调给 Go；同时把常用 InputMap action 缓存为整数 ID，降低跨 ABI 字符串开销。
// 直接调用方：生成的 spx_input_* ABI 包装和 SpxEngine 生命周期分发；
// 顶层调用方：Go runtime 输入循环、按键/鼠标积木与输入录制回放逻辑。
// Input、Camera2D 和场景树访问均应发生在 Godot 主线程。
class SpxInputMgr : public SpxManager {
public:
	// 直接调用方：SpxEngine::_notify_managers；顶层来源：Godot 启动或 Go reset/退出。
	void on_start() override;
	void on_reset(int reset_code) override;
	void on_destroy() override;

protected:
	// 场景树拥有的输入代理裸指针；本类创建并 queue_free，reset 后立即置空。
	SpxInputProxy *input_proxy = nullptr;
	// action_id 到 StringName 的稳定顺序表；仅主线程读写，reset 时清空。
	Vector<StringName> action_names;
	// StringName 到 action_id 的反向索引，与 action_names 同步维护。
	HashMap<StringName, GdInt> action_ids;

public:
	// 以下 SPX_BIND 由 ABI 直接调用，顶层均为 Go InputMgr/runtime 输入查询。
	SPX_BIND GdVec2 get_global_mouse_pos();
	SPX_BIND GdBool get_key(GdInt key);
	SPX_BIND GdBool get_mouse_state(GdInt mouse_id);
	SPX_BIND GdInt get_key_state(GdInt key);
	SPX_BIND GdFloat get_axis(GdString neg_action, GdString pos_action);
	SPX_BIND GdBool is_action_pressed(GdString action);
	SPX_BIND GdBool is_action_just_pressed(GdString action);
	SPX_BIND GdBool is_action_just_released(GdString action);
	SPX_BIND GdInt register_action(GdString action);
	SPX_BIND GdFloat get_axis_id(GdInt neg_action_id, GdInt pos_action_id);
	SPX_BIND GdBool is_action_pressed_id(GdInt action_id);
	SPX_BIND GdBool is_action_just_pressed_id(GdInt action_id);
	SPX_BIND GdBool is_action_just_released_id(GdInt action_id);
	// 一次写出鼠标坐标和按键位图，供 Go 每帧热路径减少跨语言调用次数。
	SPX_BIND void write_snapshot(SPX_OUT float out[3]);

private:
	// Go 约定的“任意键”哨兵值，不对应 Godot Key 枚举成员。
	static constexpr GdInt KEY_ANY = -1;
	const StringName *get_registered_action(GdInt action_id) const;
};

#endif // SPX_INPUT_MGR_H
