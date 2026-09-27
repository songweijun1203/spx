/**************************************************************************/
/*  movie_writer_webm.cpp                                                */
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

#include "movie_writer_webm.h"
#include "core/config/project_settings.h"
#include "core/io/file_access.h"
#include "core/os/time.h"
#include "movie_recorder_manager.h"
#include "movie_utils.h"

#if defined(WEB_ENABLED) && defined(MODULE_SPX_ENABLED)
#include <emscripten.h>

// 浏览器录制桥由 Emscripten JS library 实现；C++ 只保存初始化/活动状态，不持有 JS 对象。
// 这些函数必须在浏览器主线程调用，JS cleanup 负责释放 MediaRecorder/MediaStream 资源。
extern "C" {
int godot_audio_recorder_init();
int godot_audio_recorder_start();
void godot_audio_recorder_stop();
void godot_audio_recorder_cleanup();

int godot_video_recorder_init(int p_fps);
int godot_video_recorder_start();
void godot_video_recorder_stop();
int godot_video_recorder_get_data_size();
void godot_video_recorder_download_data(const char *p_filename);
void godot_video_recorder_cleanup();
int godot_video_recorder_has_new_data();
int godot_video_recorder_has_data();
}
#endif

// 读取 Web 自动下载策略；直接调用方：initialize_spx_recorder_servers() 创建 writer。
// 顶层调用方：Godot SERVERS 模块初始化。
MovieWriterWebM::MovieWriterWebM() {
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/enable_web_auto_download")) {
		enable_auto_download = GLOBAL_GET("movie_writer/enable_web_auto_download");
	} else {
		enable_auto_download = true;
	}
}

// Web 构建由该 writer 接管所有电影路径；非 Web 构建保留不可用桩。
// 直接调用方：Godot MovieWriter 注册中心；顶层调用方：--write-movie 格式探测。
bool MovieWriterWebM::handles_file(const String &p_path) const {
	// web only support this movie writer
	// Web 只提供这一种浏览器录制 writer。
#if !defined(WEB_ENABLED) || !defined(MODULE_SPX_ENABLED)
	return false;
#else
	return true;
#endif
}

uint32_t MovieWriterWebM::get_audio_mix_rate() const {
	return 48000;
}

AudioServer::SpeakerMode MovieWriterWebM::get_audio_speaker_mode() const {
	return AudioServer::SPEAKER_MODE_STEREO;
}

void MovieWriterWebM::get_supported_extensions(List<String> *r_extensions) const {
	r_extensions->push_back("webm");
}

bool MovieWriterWebM::handles_realtime_file(const String &p_path) const {
	return handles_file(p_path);
}

// SPX 实时入口复用 Godot MovieWriter 的 begin 实现。
// 直接调用方：MovieRecorderManager::_begin()；顶层调用方：Web --write-movie 命令行录制。
Error MovieWriterWebM::begin_realtime(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) {
	return write_begin(p_movie_size, p_fps, p_base_path);
}

// 浏览器 Canvas.captureStream() 由 compositor 自主产帧，无需 C++ 每帧上传 Image。
// 直接调用方：MovieRecorderManager::_movie_frame()；顶层调用方：Godot 主循环电影帧。
void MovieWriterWebM::add_realtime_frame() {
	// Canvas.captureStream() is driven by the browser compositor.
	// Canvas.captureStream() 由浏览器合成器驱动。
}

// 直接调用方：MovieRecorderManager::_finish()；顶层调用方：停止录制/主循环退出。
void MovieWriterWebM::end_realtime() {
	write_end();
}

#if !defined(WEB_ENABLED) || !defined(MODULE_SPX_ENABLED) // give an empty implementation

// 非 Web 或未启用 SPX 模块时提供链接桩，明确返回不可用且不创建任何资源。

Error MovieWriterWebM::write_begin(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) {
	return ERR_UNAVAILABLE;
}
Error MovieWriterWebM::write_frame(const Ref<Image> &p_image, const int32_t *p_audio_data) {
	return ERR_UNAVAILABLE;
}
void MovieWriterWebM::write_end() {
}

#else //WEB_ENABLED
// 启动浏览器 MediaRecorder：优先 Canvas 视频+音频，失败时降级为仅音频。
// 直接调用方：begin_realtime() 或 Godot MovieWriter::write_begin 调度；
// 顶层调用方：浏览器宿主录制 API 或 Web --write-movie。
// 浏览器对象的创建/销毁必须成对；初始化部分失败也立即调用 cleanup。
Error MovieWriterWebM::write_begin(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) {
	base_path = p_base_path.get_basename();
	if (base_path.is_relative_path()) {
		base_path = "res://" + base_path;
	}
	base_path += ".webm";
	fps = p_fps;

	if (MovieDebugUtils::is_stdout_verbose()) {
		print_line("MovieWriterWebM: Starting web realtime recording");
	}

	if (godot_video_recorder_init(p_fps) == 1) {
		web_video_recorder_initialized = true;
		int start_result = godot_video_recorder_start();
		if (start_result == 1) {
			web_video_recording_active = true;
			if (MovieDebugUtils::is_stdout_verbose()) {
				print_line("MovieWriterWebM: Canvas video + audio recording started");
				print_line("  Using Canvas.captureStream() for video");
				print_line("  Using MediaRecorder API for audio+video combined recording");
				print_line("  Frame rate: " + itos(p_fps) + " FPS");
			}
			return OK;
		} else {
			ERR_PRINT("MovieWriterWebM: Failed to start Canvas video recording, falling back to audio-only");
			cleanup_web_video_recorder();
			setup_web_audio_recorder();
			if (web_audio_recorder_initialized) {
				int audio_start_result = godot_audio_recorder_start();
				if (audio_start_result == 1) {
					web_audio_recording_active = true;
					if (MovieDebugUtils::is_stdout_verbose()) {
						print_line("MovieWriterWebM: Fallback to audio-only recording mode");
					}
					return OK;
				}
				cleanup_web_audio_recorder();
			}
			return ERR_CANT_CREATE;
		}
	} else {
		// Initialization can fail after creating browser-side streams.
		// 初始化失败时浏览器侧也可能已经创建部分 stream，必须无条件清理。
		godot_video_recorder_cleanup();
		ERR_PRINT("MovieWriterWebM: Canvas video recording not supported, falling back to audio-only");
		setup_web_audio_recorder();
		if (web_audio_recorder_initialized) {
			int start_result = godot_audio_recorder_start();
			if (start_result == 1) {
				web_audio_recording_active = true;
				if (MovieDebugUtils::is_stdout_verbose()) {
					print_line("MovieWriterWebM: Web audio-only recording started");
				}
				return OK;
			}
			cleanup_web_audio_recorder();
		}
		return ERR_CANT_CREATE;
	}
}

// Godot MovieWriter 逐帧回调。
// 视频模式由浏览器直接捕获 Canvas；仅音频模式只检查 JS recorder 是否仍可消费。
// 直接调用方：Godot 电影循环；顶层调用方：每个录制帧。
Error MovieWriterWebM::write_frame(const Ref<Image> &p_image, const int32_t *p_audio_data) {
	if (web_video_recording_active) {
		return OK;
	} else if (web_audio_recording_active) {
		bool has_audio_data = process_web_audio_data();
		return has_audio_data ? OK : ERR_CANT_ACQUIRE_RESOURCE;
	}

	return ERR_UNAVAILABLE;
}

// 停止所有已初始化的浏览器 recorder。
// 直接调用方：end_realtime()/Godot MovieWriter 结束流程；顶层调用方：停止录制或退出。
void MovieWriterWebM::write_end() {
	if (web_video_recorder_initialized) {
		cleanup_web_video_recorder();
	}
	if (web_audio_recorder_initialized) {
		cleanup_web_audio_recorder();
	}
}

// 建立仅音频 MediaRecorder；直接调用方：write_begin() 的视频降级路径。
void MovieWriterWebM::setup_web_audio_recorder() {
	if (web_audio_recorder_initialized) {
		if (MovieDebugUtils::is_stdout_verbose()) {
			print_line("MovieWriterWebM: Web audio recorder already initialized");
		}
		return;
	}

	int result = godot_audio_recorder_init();
	if (result == 1) {
		web_audio_recorder_initialized = true;
		if (MovieDebugUtils::is_stdout_verbose()) {
			print_line("MovieWriterWebM: Web audio recorder initialized successfully");
		}

	} else {
		// Initialization can fail after creating a MediaStreamDestination.
		// 即使 init 返回失败，也可能已经创建 MediaStreamDestination，必须调用 JS cleanup。
		godot_audio_recorder_cleanup();
		ERR_PRINT("MovieWriterWebM: Failed to initialize web audio recorder");
		web_audio_recorder_initialized = false;
	}
}

// 先 stop 活动 recorder，再释放浏览器对象和 C++ 复用缓冲；允许重复调用。
// 直接调用方：write_end()/write_begin() 失败清理。
void MovieWriterWebM::cleanup_web_audio_recorder() {
	if (!web_audio_recorder_initialized) {
		return;
	}

	if (web_audio_recording_active) {
		godot_audio_recorder_stop();
		web_audio_recording_active = false;
	}

	godot_audio_recorder_cleanup();
	web_audio_recorder_initialized = false;
	web_audio_buffer.clear();

	if (MovieDebugUtils::is_stdout_verbose()) {
		print_line("MovieWriterWebM: Web audio recorder cleaned up");
	}
}

// 仅音频模式的状态检查；实际音频由浏览器 WebAudio/MediaRecorder 链路采集。
bool MovieWriterWebM::process_web_audio_data() {
	if (!web_audio_recorder_initialized || !web_audio_recording_active) {
		return false;
	}
	return true;
}

// 初始化 Canvas captureStream recorder；当前主启动路径内联同样流程，本函数供兼容调用。
void MovieWriterWebM::setup_web_video_recorder(uint32_t p_fps) {
	if (godot_video_recorder_init(p_fps) == 1) {
		web_video_recorder_initialized = true;
		if (MovieDebugUtils::is_stdout_verbose()) {
			print_line("MovieWriterWebM: Canvas video recorder initialized successfully");
		}
	} else {
		web_video_recorder_initialized = false;
		ERR_PRINT("MovieWriterWebM: Failed to initialize Canvas video recorder");
	}
}

// 停止 Canvas recorder，按配置触发 Blob 下载，再释放 JS 资源。
// 直接调用方：write_end()/write_begin() 失败分支；顶层调用方：录制结束。
void MovieWriterWebM::cleanup_web_video_recorder() {
	if (web_video_recording_active) {
		godot_video_recorder_stop();
		web_video_recording_active = false;

		if (godot_video_recorder_has_data() == 1 && enable_auto_download) {
			int data_size = godot_video_recorder_get_data_size();
			if (MovieDebugUtils::is_stdout_verbose()) {
				print_line(String("MovieWriterWebM: Canvas video recording completed. Data size: ") + String::humanize_size(data_size));
			}

			String filename = String("spx_recording_") + Time::get_singleton()->get_datetime_string_from_system(false, true) + ".webm";
			godot_video_recorder_download_data(filename.utf8().get_data());
			if (MovieDebugUtils::is_stdout_verbose()) {
				print_line("MovieWriterWebM: Canvas video file download initiated: " + filename);
			}
		} else {
			if (MovieDebugUtils::is_stdout_verbose()) {
				print_line("MovieWriterWebM: No Canvas video data to download");
			}
		}
	}

	if (web_video_recorder_initialized) {
		godot_video_recorder_cleanup();
		web_video_recorder_initialized = false;
		if (MovieDebugUtils::is_stdout_verbose()) {
			print_line("MovieWriterWebM: Canvas video recorder cleaned up");
		}
	}
}

bool MovieWriterWebM::process_web_video_data() {
	if (!web_video_recording_active) {
		return false;
	}

	return godot_video_recorder_has_new_data() == 1;
}

extern "C" {
// 以下 KEEPALIVE 导出供浏览器页面/JS runtime 直接调用，Emscripten 链接器不得裁剪。
// 顶层调用链：页面录制控件 -> Wasm export -> MovieRecorderManager -> MovieWriterWebM。
EMSCRIPTEN_KEEPALIVE
int godot_web_recording_request_start(const char *filename) {
	String godot_filename = String::utf8(filename ? filename : "recording");

	MovieRecorderManager::RecordingConfig config(godot_filename);
	Error result = MovieRecorderManager::start_recording(config);

	return result == OK ? 1 : 0;
}

EMSCRIPTEN_KEEPALIVE
int godot_web_recording_request_stop() {
	Error result = MovieRecorderManager::stop_recording();
	return result == OK ? 1 : 0;
}

EMSCRIPTEN_KEEPALIVE
int godot_web_recording_is_active() {
	return MovieRecorderManager::is_recording() ? 1 : 0;
}
}

#endif // WEB_ENABLED && MODULE_SPX_ENABLED
