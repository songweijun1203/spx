/**************************************************************************/
/*  spx_list_monitor.h                                                          */
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

#ifndef SPX_LIST_MONITOR_H
#define SPX_LIST_MONITOR_H

#include "scene/gui/control.h"
#include "scene/gui/scroll_bar.h"
#include "scene/resources/style_box_flat.h"

// Scratch 风格列表监视器：只绘制可见行，使节点数与文本排版成本不随列表总长度增长。
// 直接调用方是 SpxUiMgr::set_list_items；顶层调用方是 Go internal/ui 变量监视器。
// Godot 规则：自定义 Control 在 NOTIFICATION_DRAW 中绘制，状态变化后调用 queue_redraw；
// gui_input 接收滚轮事件，滚动条作为子节点由场景树拥有。
class SpxListMonitor : public Control {
	GDCLASS(SpxListMonitor, Control);

	static constexpr int ROW_HEIGHT = 24; // 每个列表数据行的逻辑像素高度。
	static constexpr int BAR_HEIGHT = 22; // 标题栏高度。
	static constexpr int FONT_SIZE = 12; // 标题及列表项的主题字体大小。
	String label; // 由 Go 传入的列表变量显示名。
	PackedStringArray items; // 当前值的本地副本，绘制帧只读取此快照。
	VScrollBar *scroll = nullptr; // 场景树拥有的滚动条子节点，本类仅借用。
	Ref<StyleBoxFlat> panel_style; // 面板背景样式的引用计数资源。
	Ref<StyleBoxFlat> row_style; // 奇偶行共用的基础样式资源。

	void update_scroll();
	void scroll_changed(double p_value);
	void draw_text(const String &p_text, const Rect2 &p_rect, const Color &p_color, HorizontalAlignment p_alignment = HORIZONTAL_ALIGNMENT_LEFT);

protected:
	static void _bind_methods();
	void _notification(int p_what); // Godot 主动派发绘制/尺寸变化通知，不由业务代码直接调用。
	void gui_input(const Ref<InputEvent> &p_event) override;

public:
	void set_items(const String &p_label, const PackedStringArray &p_items, const Color &p_color);
	SpxListMonitor();
};

#endif
