/**************************************************************************/
/*  spx_object_access.h                                                    */
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

#ifndef SPX_OBJECT_ACCESS_H
#define SPX_OBJECT_ACCESS_H

#include "core/os/thread.h"
#include "core/string/ustring.h"
#include "gdextension_spx_ext.h"

// 场景对象统一安全查找器。
// 查找前验证 Godot 主线程和 Manager 有效性；返回指针仅借用于当前调用，不加锁、
// 不延长对象生命周期，也不能跨 reset/帧保存。
// 直接调用方：下方 Sprite/UI/Audio 查找宏；顶层调用方：Go 侧对象属性与动作 API。
template <typename T, typename Mgr, typename Getter>
T *spx_checked_lookup(GdObj p_id, const char *p_context, Mgr *p_manager, Getter p_getter) {
	if (unlikely(!Thread::is_main_thread())) {
		ERR_PRINT(vformat("SPX object access in %s must run on the engine main thread.", p_context));
		return nullptr;
	}
	if (p_manager == nullptr) {
		return nullptr;
	}
	T *object = p_getter(p_manager, p_id);
	if (object == nullptr) {
		WARN_PRINT(vformat("Try to access property of a null object in %s (gid=%d)", p_context, p_id));
	}
	return object;
}

// 以下宏统一“查找失败即提前返回”的样板代码，并保留每个调用点自己的默认返回值。
// `this` 必须是相应 Manager/对象包装器；宏得到的局部指针只在当前主线程函数内有效。

// 查找 SpxSprite，失败时从 void 函数返回；直接调用方：SpxSpriteMgr 的 SPX_BIND 方法。
#define SPX_SPRITE_LOOKUP_VOID(obj, context_name)                              \
	SpxSprite *sprite = spx_checked_lookup<SpxSprite>(obj, context_name, this, \
			[](SpxSpriteMgr *mgr, GdObj id) { return mgr->get_sprite(id); });  \
	if (sprite == nullptr) {                                                   \
		return;                                                                \
	}

// 查找 SpxSprite，失败时返回指定默认值。
#define SPX_SPRITE_LOOKUP_RETURN(obj, context_name, return_val)                \
	SpxSprite *sprite = spx_checked_lookup<SpxSprite>(obj, context_name, this, \
			[](SpxSpriteMgr *mgr, GdObj id) { return mgr->get_sprite(id); });  \
	if (sprite == nullptr) {                                                   \
		return return_val;                                                     \
	}

// 查找作为目标参数传入的另一精灵，局部变量名固定为 sprite_target。
#define SPX_TARGET_SPRITE_LOOKUP_VOID(target_obj, context_name)                              \
	SpxSprite *sprite_target = spx_checked_lookup<SpxSprite>(target_obj, context_name, this, \
			[](SpxSpriteMgr *mgr, GdObj id) { return mgr->get_sprite(id); });                \
	if (sprite_target == nullptr) {                                                          \
		return;                                                                              \
	}

#define SPX_TARGET_SPRITE_LOOKUP_RETURN(target_obj, context_name, return_val)                \
	SpxSprite *sprite_target = spx_checked_lookup<SpxSprite>(target_obj, context_name, this, \
			[](SpxSpriteMgr *mgr, GdObj id) { return mgr->get_sprite(id); });                \
	if (sprite_target == nullptr) {                                                          \
		return return_val;                                                                   \
	}

// 查找 SpxUi 包装对象；直接调用方：SpxUiMgr 的 SPX_BIND 方法。
#define SPX_UI_LOOKUP_VOID(obj, context_name)                           \
	SpxUi *node = spx_checked_lookup<SpxUi>(obj, context_name, this,    \
			[](SpxUiMgr *mgr, GdObj id) { return mgr->get_node(id); }); \
	if (node == nullptr) {                                              \
		return;                                                         \
	}

#define SPX_UI_LOOKUP_RETURN(obj, context_name, return_val)             \
	SpxUi *node = spx_checked_lookup<SpxUi>(obj, context_name, this,    \
			[](SpxUiMgr *mgr, GdObj id) { return mgr->get_node(id); }); \
	if (node == nullptr) {                                              \
		return return_val;                                              \
	}

// 查找一次音频播放实例；直接调用方：SpxAudio 的播放控制方法。
#define SPX_AUDIO_LOOKUP_VOID(aid, context_name)                                                  \
	AudioStreamPlayer2D *audio = spx_checked_lookup<AudioStreamPlayer2D>(aid, context_name, this, \
			[](SpxAudio *mgr, GdInt audio_id) { return mgr->_get_aid_audio(audio_id); });         \
	if (audio == nullptr) {                                                                       \
		return;                                                                                   \
	}

#define SPX_AUDIO_LOOKUP_RETURN(aid, context_name, return_val)                                    \
	AudioStreamPlayer2D *audio = spx_checked_lookup<AudioStreamPlayer2D>(aid, context_name, this, \
			[](SpxAudio *mgr, GdInt audio_id) { return mgr->_get_aid_audio(audio_id); });         \
	if (audio == nullptr) {                                                                       \
		return return_val;                                                                        \
	}

// 从 SpxUi 包装器取得实际 Godot Control；ID 参数无意义，因此固定传 0。
#define SPX_UI_CONTROL_LOOKUP_VOID(context_name)                       \
	Control *node = spx_checked_lookup<Control>(0, context_name, this, \
			[](SpxUi *ui, GdInt) { return ui->get_control_item(); });  \
	if (node == nullptr) {                                             \
		return;                                                        \
	}

#define SPX_UI_CONTROL_LOOKUP_RETURN(context_name, return_val)         \
	Control *node = spx_checked_lookup<Control>(0, context_name, this, \
			[](SpxUi *ui, GdInt) { return ui->get_control_item(); });  \
	if (node == nullptr) {                                             \
		return return_val;                                             \
	}

#endif // SPX_OBJECT_ACCESS_H
