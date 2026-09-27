/**************************************************************************/
/*  register_recorder_types.cpp                                          */
/**************************************************************************/

#include "register_recorder_types.h"

#include "core/config/project_settings.h"
#include "movie_recorder_manager.h"
#include "servers/movie_writer/movie_writer.h"
#include "spx_realtime_recorder.h"

#ifdef WEB_ENABLED
#include "movie_writer_webm.h"
#else
#include "movie_writer_obs_runtime.h"
#endif

#ifdef WEB_ENABLED
// Web MovieWriter 的模块拥有型实例；在 SERVERS 初始化创建、反初始化销毁。
static MovieWriterWebM *writer_webm = nullptr;
#else
// Native OBS 风格 MovieWriter 的模块拥有型实例；生命周期覆盖 MovieRecorderManager 回调注册期。
static ObsStyleMovieWriter *writer_obs = nullptr;
#endif

// 注册录制相关 ProjectSettings 默认值。
// 直接调用方：initialize_spx_module(CORE)；顶层调用方：Godot 模块 CORE 初始化流程。
// Godot 规则：GLOBAL_DEF 应在 CORE 阶段完成，使后续 Server/writer 初始化能读取稳定配置。
void initialize_spx_recorder_core() {
	GLOBAL_DEF("movie_writer/realtime_mode", false);
	GLOBAL_DEF("movie_writer/obs_mode", false);
	GLOBAL_DEF("movie_writer/enable_audio_playback", true);
	GLOBAL_DEF("movie_writer/enable_web_auto_download", true);
	GLOBAL_DEF(PropertyInfo(Variant::INT, "movie_writer/obs_video_fps", PROPERTY_HINT_RANGE, "10,120,1,suffix:FPS"), 30);
	GLOBAL_DEF(PropertyInfo(Variant::FLOAT, "movie_writer/obs_video_quality", PROPERTY_HINT_RANGE, "0.1,1.0,0.01"), 0.85);
	GLOBAL_DEF(PropertyInfo(Variant::INT, "movie_writer/obs_audio_sample_rate", PROPERTY_HINT_RANGE, "8000,192000,1,suffix:Hz"), 48000);
	GLOBAL_DEF(PropertyInfo(Variant::INT, "movie_writer/obs_audio_channels", PROPERTY_HINT_RANGE, "2,2,1"), 2);
	GLOBAL_DEF("movie_writer/obs_enable_timestamp_chunks", true);
	GLOBAL_DEF("movie_writer/obs_enable_repeat_frame_marking", true);
	GLOBAL_DEF("movie_writer/obs_enable_debug_output", false);
	GLOBAL_DEF("movie_writer/obs_enable_post_merge", true);
	GLOBAL_DEF("movie_writer/obs_keep_intermediate_files", false);
	GLOBAL_DEF("movie_writer/obs_ffmpeg_path", "ffmpeg");
}

// CORE 反初始化兜底停止录制；直接调用方：uninitialize_spx_module(CORE)。
// shutdown() 幂等，正常情况下 SERVERS 反初始化已先完成实际清理。
void uninitialize_spx_recorder_core() {
	MovieRecorderManager::shutdown();
}

// 创建平台对应 MovieWriter，并把 SPX 录制回调接入主循环阶段总线。
// 直接调用方：initialize_spx_module(SERVERS)；顶层调用方：Godot 模块初始化框架。
// Godot 规则：MovieWriter 属于 Server 能力，必须在 SERVERS 级别可用后注册。
void initialize_spx_recorder_servers() {
#ifdef WEB_ENABLED
	writer_webm = memnew(MovieWriterWebM);
	MovieWriter::add_writer(writer_webm);
#else
	writer_obs = memnew(ObsStyleMovieWriter);
	MovieWriter::add_writer(writer_obs);
#endif
	MovieRecorderManager::initialize();
}

// 先结束录制并注销主循环回调，再销毁回调可能访问的 writer。
// 直接调用方：uninitialize_spx_module(SERVERS)；顶层调用方：Godot 退出/初始化失败回滚。
void uninitialize_spx_recorder_servers() {
	// Stop recording and detach main-loop callbacks before deleting the writer
	// selected by those callbacks. Core teardown calls shutdown again safely.
	// Godot 生命周期要求：回调总线注销必须早于目标对象释放；CORE 阶段再次 shutdown 是幂等兜底。
	MovieRecorderManager::shutdown();
#ifdef WEB_ENABLED
	memdelete(writer_webm);
	writer_webm = nullptr;
#else
	memdelete(writer_obs);
	writer_obs = nullptr;
#endif
}

// 判断本模块是否接管给定电影路径。
// 直接调用方：MovieRecorderManager 的 claim/live-audio 回调；顶层调用方：Godot --write-movie 探测。
bool spx_recorder_claims_movie(const String &p_path) {
	if (p_path.is_empty()) {
		return false;
	}
#ifdef WEB_ENABLED
	return true;
#else
	if (p_path.get_extension().to_lower() != "avi") {
		return false;
	}
	return bool(GLOBAL_GET("movie_writer/obs_mode")) || bool(GLOBAL_GET("movie_writer/realtime_mode"));
#endif
}

// 按平台和路径选择当前注册的实时 recorder，返回模块拥有对象的借用指针。
// 直接调用方：MovieRecorderManager::_begin()；顶层调用方：Go/Web 主动录制或命令行录制。
SpxRealtimeRecorder *spx_find_realtime_movie_recorder(const String &p_path) {
#ifdef WEB_ENABLED
	return writer_webm != nullptr && writer_webm->handles_realtime_file(p_path) ? writer_webm : nullptr;
#else
	return writer_obs != nullptr && writer_obs->handles_realtime_file(p_path) ? writer_obs : nullptr;
#endif
}
