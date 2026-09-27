/**************************************************************************/
/*  movie_recorder_manager.cpp                                           */
/**************************************************************************/

#include "movie_recorder_manager.h"

#include "core/os/os.h"
#include "core/os/time.h"
#include "main/main_loop_phase_callback_bus.h"
#include "register_recorder_types.h"
#include "servers/display_server.h"
#include "spx_realtime_recorder.h"

// 本文件是录制总入口：把 Godot 命令行 MovieWriter 生命周期和浏览器宿主主动录制 API
// 统一为 SpxRealtimeRecorder 的 begin/add/end 三阶段，并确保 writer 销毁前注销回调。

// 以下静态状态均由 Godot 主线程读写；instance 是注册中心拥有 writer 的借用指针。
SpxRealtimeRecorder *MovieRecorderManager::instance = nullptr;
MovieRecorderManager::InstanceState MovieRecorderManager::state = NONE;
MovieRecorderManager::RecordingConfig MovieRecorderManager::current_config;
Size2i MovieRecorderManager::default_movie_size;
uint32_t MovieRecorderManager::default_fps = 60;
uint64_t MovieRecorderManager::recording_start_time = 0;
uint64_t MovieRecorderManager::callback_registration = MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID;
bool MovieRecorderManager::command_line_recording = false;

// 注册电影探测、开始、逐帧、结束和销毁回调。
// 直接调用方：initialize_spx_recorder_servers()；顶层调用方：Godot SERVERS 模块初始化。
// Godot 规则：MainLoopPhaseCallbackBus 只保存函数地址，shutdown 必须在 writer 销毁前注销。
void MovieRecorderManager::initialize() {
	if (callback_registration != MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID) {
		return;
	}

	MainLoopPhaseCallbackBus::Callbacks callbacks;
	callbacks.movie_claim = _movie_claim;
	callbacks.movie_requires_live_audio = _movie_requires_live_audio;
	callbacks.movie_begin = _movie_begin;
	callbacks.movie_frame = _movie_frame;
	callbacks.movie_end = _movie_end;
	callbacks.destroy = _destroy;
	callback_registration = get_main_loop_phase_callback_bus().register_callbacks(callbacks);
}

// 幂等停止当前会话并注销主循环回调。
// 直接调用方：recorder 的 SERVERS/CORE 反初始化；顶层调用方：Godot 退出流程。
void MovieRecorderManager::shutdown() {
	_finish();
	if (callback_registration != MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID) {
		get_main_loop_phase_callback_bus().unregister_callbacks(callback_registration);
		callback_registration = MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID;
	}
}

// 选择 writer 并启动统一实时录制会话。
// 直接调用方：start_recording()/_movie_begin()；顶层调用方：Native 主动录制入口，或
// cmd/spx 与 Web 启动参数注入的 Godot --write-movie。
Error MovieRecorderManager::_begin(const Size2i &p_movie_size, uint32_t p_fps, const String &p_path, bool p_command_line) {
	if (state == STARTED) {
		return ERR_ALREADY_IN_USE;
	}
	ERR_FAIL_COND_V(p_path.is_empty(), ERR_INVALID_PARAMETER);

	instance = spx_find_realtime_movie_recorder(p_path);
	ERR_FAIL_NULL_V_MSG(instance, ERR_FILE_UNRECOGNIZED, "No SPX realtime recorder handles: " + p_path);

	Error err = instance->begin_realtime(p_movie_size, p_fps, p_path);
	if (err != OK) {
		instance = nullptr;
		return err;
	}

	current_config.output_path = p_path;
	current_config.video_fps = p_fps;
	state = STARTED;
	command_line_recording = p_command_line;
	recording_start_time = Time::get_singleton()->get_ticks_usec();
	return OK;
}

// SPX 主动录制入口。
// 直接调用方：Web 导出的 godot_web_recording_request_start（Native 侧预留同类 API）；
// 顶层调用方：浏览器 Game.startRecording()/宿主录屏按钮。
// Web MediaRecorder 由 JavaScript 实际启动，C++ 只维护会话状态；Native 走 _begin()。
Error MovieRecorderManager::start_recording(const RecordingConfig &p_config) {
	if (state == STARTED) {
		return ERR_ALREADY_IN_USE;
	}
	ERR_FAIL_COND_V(p_config.output_path.is_empty(), ERR_INVALID_PARAMETER);
	current_config = p_config;

#ifdef WEB_ENABLED
	// The public Web API starts MediaRecorder in JavaScript. C++ only owns its
	// state; command-line Web recording still uses _begin() through the bus.
	// Web 公共 API 的 MediaRecorder 由 JavaScript 启动；C++ 只维护状态。
	// Web 命令行录制仍由主循环总线进入 _begin()。
	state = STARTED;
	command_line_recording = false;
	recording_start_time = Time::get_singleton()->get_ticks_usec();
	return OK;
#else
	Size2i size(p_config.video_width, p_config.video_height);
	if (size.x <= 0 || size.y <= 0) {
		size = default_movie_size;
		if (DisplayServer::get_singleton() != nullptr) {
			size = DisplayServer::get_singleton()->window_get_size();
		}
	}
	const uint32_t fps = p_config.video_fps > 0 ? p_config.video_fps : default_fps;
	return _begin(size, fps, p_config.output_path, false);
#endif
}

// 统一结束入口，最多调用一次 recorder::end_realtime()，随后清空所有会话状态。
// 直接调用方：stop_recording()、命令行 movie_end、destroy、shutdown。
void MovieRecorderManager::_finish() {
	if (state != STARTED) {
		return;
	}
	if (instance != nullptr) {
		instance->end_realtime();
	}
	instance = nullptr;
	state = NONE;
	recording_start_time = 0;
	command_line_recording = false;
}

// 主动停止录制；直接调用方：Web 导出/上层录制 API，顶层调用方：浏览器宿主停止录屏。
Error MovieRecorderManager::stop_recording() {
	if (state != STARTED) {
		return ERR_INVALID_PARAMETER;
	}
	_finish();
	return OK;
}

Error MovieRecorderManager::pause_recording() {
	return ERR_UNAVAILABLE;
}

Error MovieRecorderManager::resume_recording() {
	return ERR_UNAVAILABLE;
}

bool MovieRecorderManager::is_recording() {
	return state == STARTED;
}

bool MovieRecorderManager::is_initialized() {
	return callback_registration != MainLoopPhaseCallbackBus::INVALID_REGISTRATION_ID;
}

bool MovieRecorderManager::has_active_instance() {
	return instance != nullptr;
}

String MovieRecorderManager::get_current_output_path() {
	return current_config.output_path;
}

float MovieRecorderManager::get_recording_duration() {
	if (state != STARTED || recording_start_time == 0) {
		return 0.0f;
	}
	return float(Time::get_singleton()->get_ticks_usec() - recording_start_time) / 1000000.0f;
}

// Godot 询问当前路径是否由 SPX writer 接管。
// 直接调用方：MainLoopPhaseCallbackBus；顶层调用方：Godot --write-movie 初始化。
bool MovieRecorderManager::_movie_claim(void *p_userdata, const String &p_movie_path) {
	return spx_recorder_claims_movie(p_movie_path);
}

// 告诉 Godot 本 writer 需要实时音频链路；调用链同 _movie_claim()。
bool MovieRecorderManager::_movie_requires_live_audio(void *p_userdata, const String &p_movie_path) {
	return spx_recorder_claims_movie(p_movie_path);
}

// 命令行录制开始回调。
// 直接调用方：MainLoopPhaseCallbackBus；顶层调用方：Godot MovieWriter 启动流程。
Error MovieRecorderManager::_movie_begin(void *p_userdata, const Size2i &p_movie_size, uint32_t p_fps, const String &p_movie_path) {
	default_movie_size = p_movie_size;
	default_fps = p_fps > 0 ? p_fps : 60;
	return _begin(p_movie_size, default_fps, p_movie_path, true);
}

// draw 完成后的逐帧入口，将根 Viewport 最新画面交给实时 writer。
// 直接调用方：MainLoopPhaseCallbackBus；顶层调用方：Godot 每帧电影录制循环。
void MovieRecorderManager::_movie_frame(void *p_userdata) {
	if (state == STARTED && instance != nullptr) {
		instance->add_realtime_frame();
	}
}

// 命令行电影结束回调；主动录制不会被该回调误停。
// 直接调用方：MainLoopPhaseCallbackBus；顶层调用方：Godot MovieWriter 结束流程。
void MovieRecorderManager::_movie_end(void *p_userdata) {
	if (command_line_recording) {
		_finish();
	}
}

// 主循环销毁兜底；直接调用方：MainLoopPhaseCallbackBus，顶层调用方：Godot main 退出。
void MovieRecorderManager::_destroy(void *p_userdata) {
	_finish();
}
