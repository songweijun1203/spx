/**************************************************************************/
/*  spx_ui.h                                                              */
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

#ifndef SPX_UI_H
#define SPX_UI_H

#include "core/object/object_id.h"
#include "gdextension_spx_ext.h"
#include "scene/gui/button.h"
#include "scene/gui/check_button.h"
#include "scene/gui/control.h"
#include "scene/gui/label.h"
#include "scene/gui/line_edit.h"
#include "scene/gui/slider.h"
#include "scene/gui/texture_rect.h"

// SPX 支持的 Godot Control 类型标签，用于在 ABI 整数与具体节点子类间安全转换。
enum class ESpxUiType {
	None = 0,
	Control = 1,
	Label = 2,
	Button = 3,
	Image = 4,
	Toggle = 5,
	Input = 6,
	Slider = 7,
};

class SpxUi;
class SpxUiBindingListener;

typedef Label SpxLabel;
typedef TextureRect SpxImage;
typedef Button SpxButton;
typedef CheckButton SpxToggle;
typedef Control SpxControl;
typedef LineEdit SpxInput;
typedef HSlider SpxSlider;

// Godot Control 的非 Object 包装器：以稳定 ObjectID 保存弱引用，并把通用 UI 属性统一成
// Go 可调用接口。直接调用方是 SpxUiMgr；顶层调用方是 Go internal/ui 和 UINode API。
// 本类不拥有 Control；节点由场景树管理，销毁须 queue_free，访问前通过 ObjectDB 重新解析。
class SpxUi {
	ObjectID control_id; // 被包装 Control 的 Godot 实例 ID；对象释放后解析为 nullptr。
	ObjectID binding_id; // 挂在 Control 下的 SpxUiBinding 实例 ID，用于校验异步信号来源。
	GdObj gid = 0; // 对应 Go UI 对象 ID，由 SpxUiMgr 分配并作为回调身份。
	GdInt type = 0; // ESpxUiType 数值，决定允许向哪种 Control 子类转换。

public:
	~SpxUi();

	SpxControl *get_control();
	SpxLabel *get_label();
	SpxImage *get_image();
	SpxButton *get_button();
	SpxToggle *get_toggle();
	SpxInput *get_input();
	SpxSlider *get_slider();

public:
	void on_start();
	void set_type(GdInt etype);

	Control *get_control_item() const;
	Error set_control_item(Control *p_control, SpxUiBindingListener *p_listener);
	// p_destroying_binding 用于 Godot PREDELETE 回调，避免再次访问正在析构的绑定节点。
	void detach_control_binding(ObjectID p_destroying_binding = ObjectID());
	bool owns_binding(ObjectID p_binding_id) const;

public:
	void set_gid(GdObj id);
	GdObj get_gid() const;

	GdInt get_type();
	void queue_free();
	// Godot 规则：queue_free 延迟到当前帧末删除节点，不能在发射信号时直接 delete。
	void set_interactable(GdBool interactable);
	GdBool is_interactable();
	void set_text(GdString text);
	GdString get_text();
	void set_rect(GdRect2 rect);
	GdRect2 get_rect();
	void set_color(GdColor color);
	GdColor get_color();
	void set_font_size(GdInt size);
	GdInt get_font_size();
	void set_font(GdString path);
	GdString get_font();
	void set_visible(GdBool visible);
	GdBool get_visible();
	void set_texture(GdString path);
	GdString get_texture();

	GdInt get_layout_direction();
	void set_layout_direction(GdInt value);
	GdInt get_layout_mode();
	void set_layout_mode(GdInt value);
	GdInt get_anchors_preset();
	void set_anchors_preset(GdInt value);

	GdVec2 get_scale();
	void set_scale(GdVec2 value);
	GdVec2 get_position();
	void set_position(GdVec2 value);
	GdVec2 get_size();
	void set_size(GdVec2 value);

	GdVec2 get_global_position();
	void set_global_position(GdVec2 value);
	GdFloat get_rotation();
	void set_rotation(GdFloat value);

	GdBool get_flip(GdBool horizontal);
	void set_flip(GdBool horizontal, GdBool is_flip);
};

#endif // SPX_UI_H
