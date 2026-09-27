/**************************************************************************/
/*  spx_audio.h                                                       */
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

#ifndef SPX_AUDIO_H
#define SPX_AUDIO_H

#include "core/object/object_id.h"
#include "core/string/string_name.h"
#include "core/templates/hash_map.h"
#include "gdextension_spx_ext.h"

class AudioStreamPlayer2D;
class Node;

// 单个 SPX 音频对象的运行时状态。它把 Go 侧的一个 Audio 对象映射为若干
// AudioStreamPlayer2D 声音实例，并通过独立 Audio Bus 实现对象级音量和声像。
// 直接调用方：SpxAudioMgr；顶层调用方：Go gdengine.AudioMgr（声音/精灵音效 API）。
// 本类会增删场景树节点并访问 AudioServer，只能在 Godot 主线程使用。
class SpxAudio {
private:
	// 一次播放对应的轻量句柄。播放器由场景树拥有，不能在此保存裸指针。
	struct Voice {
		// Godot ObjectDB ID；父节点释放播放器后可据此安全判定对象已失效。
		ObjectID player_id;
		// SPX 层的循环标记；流播放结束时由 on_update 决定是否重播。
		bool loop = false;
	};

	// aid 到声音实例的索引，仅主线程读写；Node 所有权仍属于场景树。
	// 父节点可能先于音频管理器删除子节点，因此跨帧只保存 ObjectID。
	HashMap<GdInt, Voice> voices;
	// 默认播放器父节点的 ObjectID；SpxObjectMgr 创建的 audio_root/场景树拥有它。
	ObjectID root_id;

	// 当前输出总线名；默认复用 Sfx，首次设置对象级音量/声像后切换到专用总线。
	StringName bus_name;
	// 是否持有从 SpxAudioBusPool 租出的专用总线，决定重置时是否归还。
	bool owns_dedicated_bus = false;

	// 后续新建播放器使用的播放速率；单位为 Godot pitch_scale 倍率。
	GdFloat cur_pitch = 1.0;

private:
	bool ensure_dedicated_bus();
	AudioStreamPlayer2D *_get_aid_audio(GdInt aid) const;
	void _release_voice(GdInt aid);

public:
	// 直接调用方：SpxObjectMgr 的对象创建/销毁/逐帧/重置模板流程；
	// 顶层调用方：SpxEngine 生命周期（Godot 主循环或 Go 发起的 reset）。
	void on_create(GdInt p_id, Node *p_root);
	void on_destroy();
	void on_update(float delta);
	void on_reset(int reset_code);

public:
	// 以下控制接口由 SpxAudioMgr 直接调用，顶层均来自 Go AudioMgr API。
	void stop_all();
	void set_pitch(GdFloat pitch);
	GdFloat get_pitch();
	void set_pan(GdFloat pan);
	GdFloat get_pan();
	void set_volume(GdFloat volume);
	GdFloat get_volume();

	bool play(GdInt aid, GdString path, Node *owner = nullptr, GdFloat attenuation = 1.0f, GdFloat max_distance = 2000.0f);
	bool has_audio(GdInt aid) const;
	void pause(GdInt aid);
	void resume(GdInt aid);
	void stop(GdInt aid);
	GdBool restart(GdInt aid);
	void set_loop(GdInt aid, GdBool loop);
	GdBool get_loop(GdInt aid);

	GdFloat get_timer(GdInt aid);
	void set_timer(GdInt aid, GdFloat time);
	GdBool is_playing(GdInt aid);
};

#endif // SPX_AUDIO_H
