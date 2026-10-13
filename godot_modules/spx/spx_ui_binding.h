/**************************************************************************/
/*  spx_ui_binding.h                                                       */
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

#ifndef SPX_UI_BINDING_H
#define SPX_UI_BINDING_H

#include "core/object/object_id.h"
#include "scene/main/node.h"

#include "gdextension_spx_ext.h"

class Control;

// UI 绑定事件接收接口，由 SpxUiMgr 实现；binding_id 用于拒绝已失效绑定的迟到信号。
class SpxUiBindingListener {
public:
	virtual void on_spx_ui_clicked(GdObj p_gid, ObjectID p_binding_id) = 0;
	virtual void on_spx_ui_destroyed(GdObj p_gid, ObjectID p_binding_id) = 0;
	virtual ~SpxUiBindingListener() = default;
};

// SPX Control 的内部子节点：监听标准 Button 信号和 Control 生命周期，而无需修改 Godot 原类。
// 直接调用方是 SpxUi/SpxUiMgr；顶层事件来自用户点击，最终经 SPX_CALLBACK 回到 Go UI 事件。
// Godot 规则：子 Node 随父节点销毁；NOTIFICATION_PREDELETE 中只能做解除引用等轻量清理。
class SpxUiBinding : public Node {
	GDCLASS(SpxUiBinding, Node);

	ObjectID control_id; // 父 Control 的弱 ID，不延长节点寿命。
	GdObj gid = 0; // 绑定对应的 Go UI 对象 ID。
	SpxUiBindingListener *listener = nullptr; // 借用的 SpxUiMgr 接口，detach 后必须清空。

	void _notification(int p_what);
	void _on_pressed();
	// 将 pressed 回调置于已有连接之前，使 Go 回调发生在场景脚本可能销毁节点之前。
	Error _connect_pressed_first(Control *p_control);

public:
	Error attach(Control *p_control, GdObj p_gid, SpxUiBindingListener *p_listener);
	void detach();

	Control *get_control() const;
	GdObj get_gid() const { return gid; }
};

#endif // SPX_UI_BINDING_H
