/**************************************************************************/
/*  movie_writer_obs_runtime.h                                             */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#ifndef SPX_MOVIE_WRITER_OBS_RUNTIME_H
#define SPX_MOVIE_WRITER_OBS_RUNTIME_H

#include "audio_driver_hybrid.h"
#include "core/os/mutex.h"
#include "core/os/os.h"
#include "core/os/thread.h"
#include "independent_audio_recorder.h"
#include "independent_video_recorder.h"
#include "post_merge_processor.h"
#include "servers/movie_writer/movie_writer.h"
#include "spx_realtime_recorder.h"
#include "thread_safe_frame_buffer.h"
#include <atomic>

// OBS 风格实时录制器：主线程只提交最新画面，音视频工作线程按各自固定时钟连续写入，
// 因而游戏卡顿会表现为重复视频帧，而音频不会随游戏帧率断续。
// 直接调用方：MovieRecorderManager 或 Godot MovieWriter；
// 顶层调用方：Go 录制 API、Godot --write-movie。
// Godot 规则：GDCLASS 使其可由 MovieWriter 注册中心识别；write_* 是引擎离线接口，
// begin/add/end_realtime 是 SPX 实时接口，两套入口最终共享同一资源与状态机。
class ObsStyleMovieWriter : public MovieWriter, public SpxRealtimeRecorder {
	GDCLASS(ObsStyleMovieWriter, MovieWriter);

public:
	// 独立音视频线程及后处理的完整配置。
	struct ObsRecordingConfig {
		uint32_t video_fps = 30; // 固定输出帧率。
		uint32_t video_width = 1920; // 输出宽度（像素）。
		uint32_t video_height = 1080; // 输出高度（像素）。
		float jpeg_quality = 0.85f; // MJPEG 单帧质量。

		uint32_t audio_sample_rate = 48000; // PCM 采样率。
		uint32_t audio_channels = 2; // PCM 通道数。
		uint32_t audio_chunk_ms = 10; // 工作线程单块时长（毫秒）。
		uint32_t audio_buffer_seconds = 2; // 音频环形缓冲时长（秒）。

		bool enable_timestamp_chunks = true; // 是否保存时间戳块。
		bool enable_repeat_frame_marking = true; // 是否标记重复帧。
		bool enable_audio_monitoring = false; // 是否启用音频监听统计。
		bool enable_debug_output = false; // 是否输出详细诊断。

		bool enable_post_merge = true; // 结束后是否合并独立音视频文件。
		bool keep_intermediate_files = false; // 合并后是否保留中间文件。
		String ffmpeg_path = "ffmpeg"; // FFmpeg 可执行文件路径。

		uint32_t max_frame_buffer_size = 4; // 预留的最大画面缓冲数量。
	};

	// 录制状态机。
	enum RecordingState {
		STATE_UNINITIALIZED, // Uninitialized
		STATE_INITIALIZED, // Initialized
		STATE_RECORDING, // Recording
		STATE_STOPPING, // Stopping
		STATE_ERROR // Error state
	};

private:
	ThreadSafeFrameBuffer *frame_buffer = nullptr; // 本类拥有的主线程/视频线程交换器。
	IndependentVideoRecorder *video_recorder = nullptr; // 本类拥有的视频工作线程。
	IndependentAudioRecorder *audio_recorder = nullptr; // 本类拥有的音频工作线程。
	HybridAudioDriver *hybrid_audio_driver = nullptr; // 本类拥有的 Master 总线捕获器。
	PostMergeProcessor *post_merge_processor = nullptr; // 本类拥有的停止后合并器。

	// 当前录制配置。
	ObsRecordingConfig obs_config; // 当前会话配置。

	// 会话状态和时间轴。
	RecordingState current_state = STATE_UNINITIALIZED; // 防止非法重复启动/停止。
	String output_file_path; // 用户要求的最终输出路径。
	uint32_t game_frame_sequence = 0; // 主线程提交画面的单调序号。
	uint64_t recording_start_time = 0; // OS 单调时钟起点（微秒）。

	// 音频驱动兼容状态。
	AudioDriver *original_audio_driver = nullptr; // 兼容旧方案保留的原驱动借用指针。
	bool audio_driver_replaced = false; // 是否真的发生过驱动替换，决定恢复动作。

	// 主线程提交性能统计。
	uint64_t last_add_frame_time = 0; // 最近一次主线程提交时间。
	uint32_t frames_added_count = 0; // 主线程累计提交帧数。

	// 组件创建、清理和状态转换。
	Error setup_components();
	void cleanup_components(IndependentAudioRecorder *temp_audio_recorder = nullptr);
	Error setup_audio_capture();
	void restore_audio_driver();
	void update_recording_state(RecordingState new_state);

	// 停止后的音视频合并。
	void perform_post_merge();

	// 配置校验和下发。
	Error validate_config() const;
	void apply_config_to_components();

protected:
	// Godot MovieWriter 接口；直接调用方是引擎电影录制循环。
	virtual uint32_t get_audio_mix_rate() const override;
	virtual AudioServer::SpeakerMode get_audio_speaker_mode() const override;
	virtual Error write_begin(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) override;
	virtual Error write_frame(const Ref<Image> &p_image, const int32_t *p_audio_data) override;
	virtual void write_end() override;

public:
	ObsStyleMovieWriter();
	~ObsStyleMovieWriter();

	// 格式探测由 Godot MovieWriter 注册中心和 SPX recorder 注册中心调用。
	virtual bool handles_file(const String &p_path) const override;
	virtual void get_supported_extensions(List<String> *r_extensions) const override;

	bool handles_realtime_file(const String &p_path) const override;
	// 直接调用方：MovieRecorderManager；顶层调用方：Go 录制 API/命令行录制回调。
	Error begin_realtime(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) override;
	void add_realtime_frame() override;
	void end_realtime() override;

	// 配置管理；录制开始后不应修改影响资源布局的字段。
	void set_recording_config(const ObsRecordingConfig &p_config);
	const ObsRecordingConfig &get_recording_config() const { return obs_config; }

	// 当前状态查询。
	RecordingState get_recording_state() const { return current_state; }
	String get_state_name() const;
	bool is_recording_active() const { return current_state == STATE_RECORDING; }

	// 聚合音频线程、视频线程和主线程的统计。
	struct CombinedStats {
		IndependentVideoRecorder::RecordingStats video_stats; // 视频线程统计快照。
		IndependentAudioRecorder::AudioStats audio_stats; // 音频线程统计快照。
		uint32_t game_frames_added = 0; // 主线程提交次数。
		uint64_t total_recording_duration_us = 0; // 会话总时长。
		float overall_repeat_frame_ratio = 0.0f; // 重复视频帧比例。
	};

	CombinedStats get_combined_statistics() const;

	// 调试摘要与停止后的统计打印。
	String get_comprehensive_debug_info() const;
	void print_recording_summary() const;

	// 暂停/恢复控制。
	Error pause_recording();
	Error resume_recording();
	bool is_paused() const;

	// 常用质量预设，只构造配置，不启动录制。
	static ObsRecordingConfig get_high_quality_config();
	static ObsRecordingConfig get_standard_config();
	static ObsRecordingConfig get_performance_config();

	// 文件扩展名能力判断。
	static bool is_supported_format(const String &p_extension);
};

#endif // SPX_MOVIE_WRITER_OBS_RUNTIME_H
