/**************************************************************************/
/*  movie_writer_obs_runtime.cpp                                           */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#include "movie_writer_obs_runtime.h"
#include "core/config/project_settings.h"
#include "core/string/print_string.h"
#include "movie_utils.h"
#include "servers/audio_server.h"
#include "servers/display_server.h"
#include "servers/rendering_server.h"

// Native 实时录制总实现：Godot 主线程抓取最新 Viewport 画面，视频/音频线程独立定时写盘，
// 停止后再把两个单流 AVI 合并。这样游戏 update 卡顿不会阻塞音频线程或破坏输出时间轴。

// 从 CORE 阶段注册的 ProjectSettings 生成会话默认配置，并创建后处理器。
// 直接调用方：initialize_spx_recorder_servers()；顶层调用方：Godot SERVERS 初始化。
ObsStyleMovieWriter::ObsStyleMovieWriter() :
		current_state(STATE_UNINITIALIZED),
		game_frame_sequence(0),
		recording_start_time(0),
		audio_driver_replaced(false),
		last_add_frame_time(0),
		frames_added_count(0) {
	// Load configuration from project settings
	// 先加载内置标准配置，再用项目设置逐项覆盖。
	obs_config = get_standard_config();

	// Apply project settings
	// ProjectSettings 在 CORE 阶段已注册，此处 SERVERS 阶段只读取。
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_video_fps")) {
		obs_config.video_fps = GLOBAL_GET("movie_writer/obs_video_fps");
	}
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_video_quality")) {
		obs_config.jpeg_quality = GLOBAL_GET("movie_writer/obs_video_quality");
	}
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_audio_sample_rate")) {
		obs_config.audio_sample_rate = GLOBAL_GET("movie_writer/obs_audio_sample_rate");
	}
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_audio_channels")) {
		obs_config.audio_channels = GLOBAL_GET("movie_writer/obs_audio_channels");
	}
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_enable_timestamp_chunks")) {
		obs_config.enable_timestamp_chunks = GLOBAL_GET("movie_writer/obs_enable_timestamp_chunks");
	}
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_enable_repeat_frame_marking")) {
		obs_config.enable_repeat_frame_marking = GLOBAL_GET("movie_writer/obs_enable_repeat_frame_marking");
	}
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_enable_post_merge")) {
		obs_config.enable_post_merge = GLOBAL_GET("movie_writer/obs_enable_post_merge");
	}
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_keep_intermediate_files")) {
		obs_config.keep_intermediate_files = GLOBAL_GET("movie_writer/obs_keep_intermediate_files");
	}
	if (ProjectSettings::get_singleton()->has_setting("movie_writer/obs_ffmpeg_path")) {
		obs_config.ffmpeg_path = GLOBAL_GET("movie_writer/obs_ffmpeg_path");
	}
	// Initialize post-merge processor
	// 后处理器由本 writer 独占，录制组件清理时删除。
	post_merge_processor = new PostMergeProcessor();
}

// 析构兜底结束录制，并保证音频效果、worker 和 FileAccess 全部先于 writer 释放。
ObsStyleMovieWriter::~ObsStyleMovieWriter() {
	if (current_state == STATE_RECORDING) {
		write_end();
	}
	restore_audio_driver();
	cleanup_components();
}

// Godot MovieWriter 格式探测入口；直接调用方：MovieWriter 注册中心，顶层调用方：--write-movie。
bool ObsStyleMovieWriter::handles_file(const String &p_path) const {
#ifdef WEB_ENABLED
	return false;
#else
	const bool enabled = bool(GLOBAL_GET("movie_writer/obs_mode")) || bool(GLOBAL_GET("movie_writer/realtime_mode"));
	return enabled && handles_realtime_file(p_path);
#endif
}

bool ObsStyleMovieWriter::handles_realtime_file(const String &p_path) const {
	return p_path.get_extension().to_lower() == "avi";
}

// SPX 实时启动直接复用 Godot MovieWriter 生命周期。
// 直接调用方：MovieRecorderManager::_begin()；顶层调用方：Native 主动录制，或
// cmd/spx 注入 --write-movie 后触发的命令行 movie_begin。
Error ObsStyleMovieWriter::begin_realtime(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) {
	return write_begin(p_movie_size, p_fps, p_base_path);
}

// 在 Godot 主线程读取主窗口 ViewportTexture，并发布给视频双缓冲。
// 直接调用方：MovieRecorderManager::_movie_frame()；顶层调用方：Godot draw 完成后的主循环回调。
// Godot 规则：RenderingServer/Viewport 抓图必须在有效主窗口生命周期内执行；HDR 图像先转为 sRGB8。
void ObsStyleMovieWriter::add_realtime_frame() {
	if (current_state != STATE_RECORDING) {
		return;
	}

	RID main_viewport = RenderingServer::get_singleton()->viewport_find_from_screen_attachment(DisplayServer::MAIN_WINDOW_ID);
	RID main_texture = RenderingServer::get_singleton()->viewport_get_texture(main_viewport);
	Ref<Image> frame = RenderingServer::get_singleton()->texture_2d_get(main_texture);
	if (frame.is_valid() && RenderingServer::get_singleton()->viewport_is_using_hdr_2d(main_viewport)) {
		frame->convert(Image::FORMAT_RGBA8);
		frame->linear_to_srgb();
	}
	write_frame(frame, nullptr);
}

// 直接调用方：MovieRecorderManager::_finish()；顶层调用方：停止录制或 Godot 退出。
void ObsStyleMovieWriter::end_realtime() {
	write_end();
}

void ObsStyleMovieWriter::get_supported_extensions(List<String> *r_extensions) const {
	r_extensions->push_back("avi");
}

bool ObsStyleMovieWriter::is_supported_format(const String &p_extension) {
	return p_extension.to_lower() == "avi";
}

uint32_t ObsStyleMovieWriter::get_audio_mix_rate() const {
	return obs_config.audio_sample_rate;
}

AudioServer::SpeakerMode ObsStyleMovieWriter::get_audio_speaker_mode() const {
	return obs_config.audio_channels == 2 ? AudioServer::SPEAKER_MODE_STEREO : AudioServer::SPEAKER_SURROUND_31;
}

// 建立一场录制会话：校验配置、创建组件、安装 Master Bus 效果，再启动音视频线程。
// 直接调用方：begin_realtime() 或 Godot MovieWriter 调度；顶层调用方：Native API，或
// cmd/spx 注入的 --write-movie。
// 本入口及错误回滚必须在 Godot 主线程运行，因为会修改 AudioServer Bus 和录制状态。
Error ObsStyleMovieWriter::write_begin(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) {
	if (current_state != STATE_UNINITIALIZED) {
		ERR_PRINT("ObsStyleMovieWriter: Recorder state is incorrect");
		return ERR_INVALID_PARAMETER;
	}

	output_file_path = p_base_path;

	// Update video resolution in config
	// Godot 传入的实际电影尺寸覆盖会话配置。
	obs_config.video_width = p_movie_size.width;
	obs_config.video_height = p_movie_size.height;

	if (obs_config.enable_debug_output) {
		print_line("=== OBS-style Recording Started ===");
		print_line(String("Output file: ") + output_file_path);
		print_line(String("Video resolution: ") + String::num_int64(obs_config.video_width) + "x" + String::num_int64(obs_config.video_height));
		print_line(String("Target FPS: ") + String::num_int64(obs_config.video_fps));
		print_line(String("Audio config: ") + String::num_int64(obs_config.audio_sample_rate) + "Hz, " + String::num_int64(obs_config.audio_channels) + "ch");
	}

	// Validate configuration
	// 先校验，避免创建部分 FileAccess/线程资源后才发现参数非法。
	Error config_error = validate_config();
	if (config_error != OK) {
		return config_error;
	}

	// Set up recording components
	// 打开独立音视频输出并建立双缓冲。
	Error setup_error = setup_components();
	if (setup_error != OK) {
		cleanup_components();
		return setup_error;
	}

	// Set up audio capture
	// 安装 Master Bus AudioEffect，并把音频 recorder 注册为借用消费者。
	Error audio_error = setup_audio_capture();
	if (audio_error != OK) {
		restore_audio_driver();
		cleanup_components();
		return audio_error;
	}
	// Start independent recording threads
	// 两个 Godot Thread 均成功启动后才进入 STATE_RECORDING。
	Error video_start_error = video_recorder->start_recording();
	if (video_start_error != OK) {
		ERR_PRINT("ObsStyleMovieWriter: Failed to start video recording");
		restore_audio_driver();
		cleanup_components();
		return video_start_error;
	}

	Error audio_start_error = audio_recorder->start_recording();
	if (audio_start_error != OK) {
		ERR_PRINT("ObsStyleMovieWriter: Failed to start audio recording");
		video_recorder->stop_recording();
		restore_audio_driver();
		cleanup_components();
		return audio_start_error;
	}

	// Update state
	// 状态最后提交，确保 write_frame 不会看到半初始化组件。
	update_recording_state(STATE_RECORDING);
	recording_start_time = OS::get_singleton()->get_ticks_usec();
	game_frame_sequence = 0;
	frames_added_count = 0;

	return OK;
}

// Godot 主线程逐帧提交入口，只发布最新 Image；PCM 参数在独立音频模式下不使用。
// 直接调用方：add_realtime_frame() 或 Godot MovieWriter 电影循环；顶层调用方：每个录制帧。
Error ObsStyleMovieWriter::write_frame(const Ref<Image> &p_image, const int32_t *p_audio_data) {
	if (current_state != STATE_RECORDING) {
		return ERR_UNCONFIGURED;
	}

	uint64_t current_time = OS::get_singleton()->get_ticks_usec();

	// Update game frame data to double buffer
	// 双缓冲只保留最新游戏帧，视频线程按自己的固定 FPS 决定何时消费/重复。
	if (p_image.is_valid()) {
		frame_buffer->update_frame(p_image, current_time, game_frame_sequence);
		game_frame_sequence++;
		frames_added_count++;
	}

	last_add_frame_time = current_time;

	// Independent recording mode: audio captured by HybridAudioDriver
	// p_audio_data parameter is not used in independent recording mode
	// 音频由 Master Bus 效果实时捕获，因此忽略 Godot 离线 MovieWriter 传入的 PCM 指针。

	return OK;
}

// 按“停止并 join worker -> 移除音频效果 -> 合并文件 -> 删除组件”的顺序结束会话。
// 直接调用方：end_realtime()/Godot MovieWriter 结束回调/析构；顶层调用方：停止录制或退出。
// Godot Thread 规则：任何 recorder/delete/FileAccess close 都必须发生在 wait_to_finish() 之后。
void ObsStyleMovieWriter::write_end() {
	if (current_state != STATE_RECORDING) {
		return;
	}

	update_recording_state(STATE_STOPPING);

	// Stop independent recording threads
	// 先停止视频和音频 writer，使中间 AVI 头/索引完整落盘。
	if (video_recorder) {
		video_recorder->stop_recording();
	}

	if (audio_recorder) {
		audio_recorder->stop_recording();
	}

	// Store audio_recorder reference before restore (to prevent double cleanup)
	// 暂存拥有型指针，避免 restore 后的统一清理重复删除。
	IndependentAudioRecorder *temp_audio_recorder = audio_recorder;

	// Restore audio driver
	// 实际上是撤销模块 AudioEffect，不替换 Godot AudioDriver singleton。
	restore_audio_driver();

	// Clear audio_recorder reference to prevent double cleanup in destructor
	// 清空成员后由 temp_audio_recorder 负责唯一一次 delete。
	audio_recorder = nullptr;

	// Print recording summary
	if (obs_config.enable_debug_output) {
		print_recording_summary();
	}

	// Post-merge processing (only for desktop platforms)
	// 中间 writer 已关闭后才允许 FileAccess/FFmpeg 重新打开文件合并。
#ifndef WEB_ENABLED
	if (obs_config.enable_post_merge && post_merge_processor) {
		perform_post_merge();
	}
#endif

	// Clean up components
	// worker 已 join、音频回调已解绑，此时才能删除裸指针组件。
	cleanup_components(temp_audio_recorder); // Pass temp_audio_recorder for proper cleanup

	update_recording_state(STATE_UNINITIALIZED);

	if (obs_config.enable_debug_output) {
		print_line("=== OBS-style Recording Completed ===");
	}
}

// 分配双缓冲、视频/音频 recorder，并打开两个中间 AVI。
// 直接调用方：write_begin()；顶层调用方：录制启动。
Error ObsStyleMovieWriter::setup_components() {
	if (post_merge_processor == nullptr) {
		post_merge_processor = new PostMergeProcessor();
	}

	// Independent recording mode
	// Create double buffer
	// 双缓冲连接 Godot 主线程生产者与视频 worker 消费者。
	frame_buffer = new ThreadSafeFrameBuffer();
	if (!frame_buffer) {
		ERR_PRINT("ObsStyleMovieWriter: Failed to create frame buffer");
		return ERR_OUT_OF_MEMORY;
	}

	// Create video recorder
	video_recorder = new IndependentVideoRecorder();
	if (!video_recorder) {
		ERR_PRINT("ObsStyleMovieWriter: Failed to create video recorder");
		return ERR_OUT_OF_MEMORY;
	}

	// Configure video recorder
	IndependentVideoRecorder::RecordingConfig video_config;
	video_config.target_fps = obs_config.video_fps;
	video_config.video_width = obs_config.video_width;
	video_config.video_height = obs_config.video_height;
	video_config.jpeg_quality = obs_config.jpeg_quality;
	video_config.enable_timestamp_chunks = obs_config.enable_timestamp_chunks;
	video_config.enable_repeat_frame_marking = obs_config.enable_repeat_frame_marking;

	Error video_init_error = video_recorder->initialize(frame_buffer, output_file_path + "_video.avi", video_config);
	if (video_init_error != OK) {
		ERR_PRINT("ObsStyleMovieWriter: Video recorder initialization failed");
		return video_init_error;
	}

	// Create audio recorder
	audio_recorder = new IndependentAudioRecorder();
	if (!audio_recorder) {
		ERR_PRINT("ObsStyleMovieWriter: Failed to create audio recorder");
		return ERR_OUT_OF_MEMORY;
	}

	// Configure audio recorder
	IndependentAudioRecorder::AudioConfig audio_config;
	audio_config.sample_rate = obs_config.audio_sample_rate;
	audio_config.channels = obs_config.audio_channels;
	audio_config.chunk_size = (audio_config.sample_rate * obs_config.audio_chunk_ms) / 1000;
	audio_config.buffer_size_seconds = obs_config.audio_buffer_seconds;
	audio_config.enable_audio_monitoring = obs_config.enable_audio_monitoring;

	// Note: audio_recorder needs to be associated with HybridAudioDriver in setup_audio_capture
	// 音频 recorder 要等 setup_audio_capture 取得真实 mix rate 后再 initialize。

	update_recording_state(STATE_INITIALIZED);

	return OK;
}

// 删除已停止的组件；temp_audio_recorder 用于表达 write_end 转移出的唯一所有权。
// 直接调用方：write_begin() 错误回滚、write_end()、析构。
void ObsStyleMovieWriter::cleanup_components(IndependentAudioRecorder *temp_audio_recorder) {
	// Clean up independent recording components
	// 调用方必须已停止线程；各 recorder 析构仅作幂等兜底。
	if (video_recorder) {
		delete video_recorder;
		video_recorder = nullptr;
	}

	if (temp_audio_recorder) {
		delete temp_audio_recorder; // This calls ~IndependentAudioRecorder()
	} else if (audio_recorder) {
		delete audio_recorder; // This calls ~IndependentAudioRecorder()
		audio_recorder = nullptr;
	}

	// Ensure audio_recorder is null
	// 无论走哪个所有权分支，最终都清空成员借用状态。
	audio_recorder = nullptr;

	if (frame_buffer) {
		delete frame_buffer;
		frame_buffer = nullptr;
	}

	if (post_merge_processor) {
		delete post_merge_processor;
		post_merge_processor = nullptr;
	}

	hybrid_audio_driver = nullptr;
	current_state = STATE_UNINITIALIZED;
}

// 在桌面端安装 Master Bus 捕获效果并注册 audio_recorder。
// 直接调用方：write_begin()；顶层调用方：录制启动。
// Godot 规则：AudioServer Bus 只能在主线程变更；AudioEffect::process 随后运行在音频线程。
Error ObsStyleMovieWriter::setup_audio_capture() {
#ifdef WEB_ENABLED
	// Web platform uses MediaRecorder API audio from MovieWriter
	if (obs_config.enable_debug_output) {
		print_line("ObsStyleMovieWriter: Web platform detected, using MediaRecorder API audio from MovieWriter");
	}
	return OK;
#else
	// Desktop platform: capture Master through a module-owned AudioEffect.
	// 模块自有 AudioEffect 捕获最终 Master 混音，不替换全局 AudioDriver。
	hybrid_audio_driver = memnew(HybridAudioDriver);
	Error capture_error = hybrid_audio_driver->init(AudioServer::get_singleton()->get_mix_rate(), AudioDriver::SpeakerMode(AudioServer::get_singleton()->get_speaker_mode()));
	if (capture_error != OK) {
		memdelete(hybrid_audio_driver);
		hybrid_audio_driver = nullptr;
		return capture_error;
	}
	hybrid_audio_driver->start();

	if (obs_config.enable_debug_output) {
		print_line("ObsStyleMovieWriter: Capturing the Master bus through an AudioEffect");
	}

	// Configure audio recorder
	IndependentAudioRecorder::AudioConfig audio_config;
	audio_config.sample_rate = hybrid_audio_driver->get_mix_rate();
	audio_config.channels = obs_config.audio_channels;
	audio_config.chunk_size = (audio_config.sample_rate * obs_config.audio_chunk_ms) / 1000;
	audio_config.buffer_size_seconds = obs_config.audio_buffer_seconds;
	audio_config.enable_audio_monitoring = obs_config.enable_audio_monitoring;

	Error audio_init_error = audio_recorder->initialize(hybrid_audio_driver, output_file_path + "_audio.avi", audio_config);
	if (audio_init_error != OK) {
		ERR_PRINT("ObsStyleMovieWriter: Audio recorder initialization failed");
		return audio_init_error;
	}

	// Register audio recorder with HybridAudioDriver
	// 注册的是借用指针，restore_audio_driver 必须在 recorder delete 前撤销。
	hybrid_audio_driver->register_audio_recorder(audio_recorder);

	// Enable recording mode
	hybrid_audio_driver->enable_recording(true);

#endif

	return OK;
}

// 撤销 recorder 借用关系、停用并移除 AudioEffect，再释放捕获器。
// 直接调用方：write_end()/错误回滚/析构；必须在 Godot 主线程执行。
void ObsStyleMovieWriter::restore_audio_driver() {
	if (hybrid_audio_driver && audio_recorder) {
		hybrid_audio_driver->unregister_audio_recorder(audio_recorder);
	}
	if (hybrid_audio_driver) {
		hybrid_audio_driver->enable_recording(false);
		hybrid_audio_driver->finish();
		memdelete(hybrid_audio_driver);
		hybrid_audio_driver = nullptr;
	}
	original_audio_driver = nullptr;
}

void ObsStyleMovieWriter::update_recording_state(RecordingState new_state) {
	if (current_state != new_state) {
		current_state = new_state;

		if (obs_config.enable_debug_output) {
			print_line(String("ObsStyleMovieWriter: State changed to ") + get_state_name());
		}
	}
}

Error ObsStyleMovieWriter::validate_config() const {
	if (obs_config.video_width == 0 || obs_config.video_height == 0) {
		ERR_PRINT("ObsStyleMovieWriter: Invalid video resolution");
		return ERR_INVALID_PARAMETER;
	}

	if (obs_config.video_fps == 0 || obs_config.video_fps > 120) {
		ERR_PRINT("ObsStyleMovieWriter: Invalid video frame rate");
		return ERR_INVALID_PARAMETER;
	}

	if (obs_config.audio_sample_rate < 8000 || obs_config.audio_sample_rate > 192000) {
		ERR_PRINT("ObsStyleMovieWriter: Invalid audio sample rate");
		return ERR_INVALID_PARAMETER;
	}

	if (obs_config.audio_channels != 2) {
		ERR_PRINT("ObsStyleMovieWriter: Realtime capture currently requires stereo audio");
		return ERR_INVALID_PARAMETER;
	}

	if (obs_config.jpeg_quality < 0.1f || obs_config.jpeg_quality > 1.0f) {
		ERR_PRINT("ObsStyleMovieWriter: Invalid JPEG quality");
		return ERR_INVALID_PARAMETER;
	}

	return OK;
}

String ObsStyleMovieWriter::get_state_name() const {
	switch (current_state) {
		case STATE_UNINITIALIZED:
			return "Uninitialized";
		case STATE_INITIALIZED:
			return "Initialized";
		case STATE_RECORDING:
			return "Recording";
		case STATE_STOPPING:
			return "Stopping";
		case STATE_ERROR:
			return "Error state";
		default:
			return "Unknown state";
	}
}

// Configuration presets
ObsStyleMovieWriter::ObsRecordingConfig ObsStyleMovieWriter::get_high_quality_config() {
	ObsRecordingConfig config;
	config.video_fps = 30;
	config.jpeg_quality = 0.95f;
	config.audio_sample_rate = 48000;
	config.audio_channels = 2;
	config.audio_buffer_seconds = 3;
	config.enable_timestamp_chunks = true;
	config.enable_repeat_frame_marking = true;
	return config;
}

ObsStyleMovieWriter::ObsRecordingConfig ObsStyleMovieWriter::get_standard_config() {
	ObsRecordingConfig config;
	config.video_fps = 30;
	config.jpeg_quality = 0.85f;
	config.audio_sample_rate = 48000;
	config.audio_channels = 2;
	config.audio_buffer_seconds = 2;
	config.enable_timestamp_chunks = true;
	config.enable_repeat_frame_marking = true;
	return config;
}

ObsStyleMovieWriter::ObsRecordingConfig ObsStyleMovieWriter::get_performance_config() {
	ObsRecordingConfig config;
	config.video_fps = 30;
	config.jpeg_quality = 0.75f;
	config.audio_sample_rate = 44100;
	config.audio_channels = 2;
	config.audio_buffer_seconds = 1;
	config.enable_timestamp_chunks = false;
	config.enable_repeat_frame_marking = true;
	config.enable_debug_output = false;
	return config;
}

// Statistics and debug features
ObsStyleMovieWriter::CombinedStats ObsStyleMovieWriter::get_combined_statistics() const {
	CombinedStats stats;

	if (video_recorder) {
		stats.video_stats = video_recorder->get_statistics();
	}

	if (audio_recorder) {
		stats.audio_stats = audio_recorder->get_statistics();
	}

	stats.game_frames_added = frames_added_count;
	stats.total_recording_duration_us = OS::get_singleton()->get_ticks_usec() - recording_start_time;

	if (video_recorder) {
		stats.overall_repeat_frame_ratio = video_recorder->get_repeat_frame_ratio();
	}

	return stats;
}

String ObsStyleMovieWriter::get_comprehensive_debug_info() const {
	String info;
	info += "=== ObsStyleMovieWriter Comprehensive Debug Info ===\n";
	info += String("Current state: ") + get_state_name() + "\n";
	info += String("Output file: ") + output_file_path + "\n";
	info += String("Game frame sequence: ") + String::num_int64(game_frame_sequence) + "\n";
	info += String("Game frames added: ") + String::num_int64(frames_added_count) + "\n";

	if (recording_start_time > 0) {
		uint64_t duration = OS::get_singleton()->get_ticks_usec() - recording_start_time;
		info += String("Recording duration: ") + String::num_real(duration / 1000000.0) + " seconds\n";
	}

	info += "\n--- Recording Configuration ---\n";
	info += String("Video FPS: ") + String::num_int64(obs_config.video_fps) + "\n";
	info += String("Video resolution: ") + String::num_int64(obs_config.video_width) + "x" + String::num_int64(obs_config.video_height) + "\n";
	info += String("JPEG quality: ") + String::num_real(obs_config.jpeg_quality) + "\n";
	info += String("Audio sample rate: ") + String::num_int64(obs_config.audio_sample_rate) + "Hz\n";
	info += String("Audio channels: ") + String::num_int64(obs_config.audio_channels) + "\n";

	if (video_recorder) {
		info += "\n--- Video Recorder Info ---\n";
		info += video_recorder->get_debug_info() + "\n";
	}

	if (audio_recorder) {
		info += "\n--- Audio Recorder Info ---\n";
		info += audio_recorder->get_debug_info() + "\n";
	}

	if (frame_buffer) {
		info += "\n--- Frame Buffer Info ---\n";
		info += String("Total updates: ") + String::num_int64(frame_buffer->get_total_updates()) + "\n";
		info += String("Buffer switches: ") + String::num_int64(frame_buffer->get_buffer_switches()) + "\n";
		info += String("Last sequence: ") + String::num_int64(frame_buffer->get_last_sequence()) + "\n";
	}

	info += "\n=========================================\n";

	return info;
}

void ObsStyleMovieWriter::print_recording_summary() const {
	CombinedStats stats = get_combined_statistics();

	print_line("=== OBS-style Recording Summary ===");
	print_line(String("Recording duration: ") + String::num_real(stats.total_recording_duration_us / 1000000.0) + " seconds");
	print_line(String("Game frames: ") + String::num_int64(stats.game_frames_added));
	print_line(String("Recorded video frames: ") + String::num_int64(stats.video_stats.total_recorded_frames));
	print_line(String("New frames: ") + String::num_int64(stats.video_stats.new_frames_count));
	print_line(String("Repeated frames: ") + String::num_int64(stats.video_stats.repeated_frames_count));
	print_line(String("Repeated frame ratio: ") + String::num_real(stats.overall_repeat_frame_ratio * 100.0f) + "%");
	print_line(String("Audio chunks: ") + String::num_int64(stats.audio_stats.total_chunks_recorded));
	print_line(String("Audio samples: ") + String::num_int64(stats.audio_stats.total_samples_recorded));
	print_line(String("Audio buffer overruns: ") + String::num_int64(stats.audio_stats.buffer_overruns));
	print_line(String("Audio buffer underruns: ") + String::num_int64(stats.audio_stats.buffer_underruns));

	print_line("==================");
}

void ObsStyleMovieWriter::set_recording_config(const ObsRecordingConfig &p_config) {
	if (current_state == STATE_RECORDING) {
		ERR_PRINT("ObsStyleMovieWriter: Cannot change configuration while recording");
		return;
	}

	obs_config = p_config;

	if (obs_config.enable_debug_output) {
		print_line("ObsStyleMovieWriter: Configuration updated");
	}
}

Error ObsStyleMovieWriter::pause_recording() {
	ERR_PRINT("ObsStyleMovieWriter: Pause functionality not yet implemented");
	return ERR_UNAVAILABLE;
}

Error ObsStyleMovieWriter::resume_recording() {
	ERR_PRINT("ObsStyleMovieWriter: Resume functionality not yet implemented");
	return ERR_UNAVAILABLE;
}

bool ObsStyleMovieWriter::is_paused() const {
	return false; // Pause functionality not yet implemented
}

// 同步后处理入口：选择 FFmpeg 或内建 AVI 合并器，把两个已关闭的中间文件合为最终文件。
// 直接调用方：write_end()；顶层调用方：Native API，或 cmd/spx 启动的电影录制结束。
// FileAccess/外部进程操作会阻塞当前主线程，因此必须在 worker 停止后执行，且不与 writer 并发访问文件。
void ObsStyleMovieWriter::perform_post_merge() {
	if (obs_config.enable_debug_output) {
		print_line("ObsStyleMovieWriter::perform_post_merge() called");
		print_line("  post_merge_processor: " + String(post_merge_processor ? "valid" : "null"));
		print_line("  output_file_path: " + output_file_path);
		print_line(String("  enable_post_merge: ") + (obs_config.enable_post_merge ? "true" : "false"));
	}

	if (!post_merge_processor || output_file_path.is_empty()) {
		if (obs_config.enable_debug_output) {
			print_line("PostMergeProcessor: Skipping merge - missing processor or output path");
		}
		return;
	}

	// Configure post-merge processor
	// 每次会话同步下发 FFmpeg 路径、中间文件保留和诊断策略。
	PostMergeProcessor::MergeConfig merge_config;
	merge_config.method = PostMergeProcessor::METHOD_FFMPEG_SYSTEM;
	merge_config.keep_intermediate_files = obs_config.keep_intermediate_files;
	merge_config.enable_debug_output = obs_config.enable_debug_output;
	merge_config.ffmpeg_path = obs_config.ffmpeg_path;

	// Try to use recommended method if FFmpeg is not available
	// FFmpeg 不可用时回退到内建 AVI 合并。
	PostMergeProcessor::MergeMethod recommended = post_merge_processor->get_recommended_method();
	if (recommended != PostMergeProcessor::METHOD_FFMPEG_SYSTEM) {
		merge_config.method = recommended;
		if (obs_config.enable_debug_output) {
			print_line("PostMergeProcessor: FFmpeg not available, using method: " + post_merge_processor->get_method_name(recommended));
		}
	}

	post_merge_processor->set_config(merge_config);

	// Construct file paths
	// 路径必须与 setup_components 创建中间 writer 时的后缀完全一致。
	String video_path = output_file_path + "_video.avi";
	String audio_path = output_file_path + "_audio.avi";
	String merged_path = output_file_path + "_merged.avi";

	if (obs_config.enable_debug_output) {
		print_line("PostMergeProcessor: Starting post-merge operation");
		print_line("  Video: " + video_path);
		print_line("  Audio: " + audio_path);
		print_line("  Output: " + merged_path);
	}

	// Execute merge
	// merge_files 返回结构化错误；失败时保留独立音视频文件便于恢复。
	PostMergeProcessor::MergeResult result = post_merge_processor->merge_files(video_path, audio_path, merged_path);

	if (result.error_code == OK) {
		if (obs_config.enable_debug_output) {
			print_line(String("PostMergeProcessor: Merge completed successfully in ") + String::num(result.merge_duration_seconds, 2) + " seconds");
			print_line("PostMergeProcessor: Output file: " + result.output_file_path);
			if (result.intermediate_files_cleaned) {
				print_line("PostMergeProcessor: Intermediate files cleaned up");
			}
		}
	} else {
		ERR_PRINT("PostMergeProcessor: Merge failed - " + result.error_message);
		if (obs_config.enable_debug_output) {
			print_line("PostMergeProcessor: Keeping separate video and audio files");
		}
	}
}
