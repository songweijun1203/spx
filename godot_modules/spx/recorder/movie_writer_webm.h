/**************************************************************************/
/*  movie_writer_webm.h                                                  */
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

#ifndef SPX_MOVIE_WRITER_WEBM_H
#define SPX_MOVIE_WRITER_WEBM_H

#include "core/error/error_macros.h"
#include "core/io/image.h"
#include "core/math/vector2i.h"
#include "core/string/ustring.h"
#include "core/templates/list.h"
#include "modules/modules_enabled.gen.h"
#include "servers/audio_server.h"
#include "servers/movie_writer/movie_writer.h"
#include "spx_realtime_recorder.h"

// Web 平台录制适配器，将 Godot/SPX 的录制生命周期转发给浏览器 MediaRecorder 桥接层。
// 直接调用方：MovieRecorderManager 或 Godot MovieWriter；顶层调用方：Go 录制 API/--write-movie。
// Godot 规则：MovieWriter 派生类由注册中心按扩展名选择；浏览器对象只能在 Web 构建分支使用。
class MovieWriterWebM : public MovieWriter, public SpxRealtimeRecorder {
	GDCLASS(MovieWriterWebM, MovieWriter)

	bool enable_auto_download; // 结束后是否让浏览器自动下载生成的 Blob。
	String base_path; // 输出基础名，供 JS 侧命名下载文件。
	uint32_t fps = 30; // 浏览器视频轨目标帧率。

public:
	MovieWriterWebM();

	bool handles_realtime_file(const String &p_path) const override;
	Error begin_realtime(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) override;
	void add_realtime_frame() override;
	void end_realtime() override;

protected:
	virtual uint32_t get_audio_mix_rate() const override;
	virtual AudioServer::SpeakerMode get_audio_speaker_mode() const override;
	virtual void get_supported_extensions(List<String> *r_extensions) const override;
	virtual Error write_begin(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) override;
	virtual Error write_frame(const Ref<Image> &p_image, const int32_t *p_audio_data) override;
	virtual void write_end() override;

	virtual bool handles_file(const String &p_path) const override;

#if defined(WEB_ENABLED) && defined(MODULE_SPX_ENABLED)

private:
	bool web_audio_recorder_initialized = false; // JS 音频桥是否已创建。
	bool web_audio_recording_active = false; // 当前是否接收音频块。
	Vector<uint8_t> web_audio_buffer; // 向 JS 传递 PCM 前的复用缓冲。

	bool web_video_recorder_initialized = false; // JS 视频桥是否已创建。
	bool web_video_recording_active = false; // 当前是否采集 Canvas 视频轨。

	void setup_web_audio_recorder();
	void cleanup_web_audio_recorder();
	bool process_web_audio_data();

	void setup_web_video_recorder(uint32_t p_fps);
	void cleanup_web_video_recorder();
	bool process_web_video_data();
#endif
};

#endif // SPX_MOVIE_WRITER_WEBM_H
