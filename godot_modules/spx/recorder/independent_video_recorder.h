/**************************************************************************/
/*  independent_video_recorder.h                                         */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#ifndef SPX_INDEPENDENT_VIDEO_RECORDER_H
#define SPX_INDEPENDENT_VIDEO_RECORDER_H

#include "core/os/mutex.h"
#include "core/os/os.h"
#include "core/os/thread.h"
#include "simple_video_writer.h"
#include "thread_safe_frame_buffer.h"
#include <atomic>
#include <chrono>

// 固定帧率的独立视频录制器，从双缓冲读取游戏主线程最新画面并写入 MJPEG AVI。
// 游戏卡顿时重复最后有效帧，以保持输出时间轴连续并如实反映卡顿时长。
// 直接调用方：ObsStyleMovieWriter；顶层调用方：Go 录制 API 或 Godot 命令行电影录制。
// Godot 规则：Image 用 Ref 跨线程保活；Thread 结束前必须 wait_to_finish，统计共享需加锁。
class IndependentVideoRecorder {
public:
	// 视频录制配置，在线程启动前由 ObsStyleMovieWriter 下发。
	struct RecordingConfig {
		uint32_t target_fps; // 输出时间轴目标帧率。
		uint32_t video_width; // 输出宽度（像素）。
		uint32_t video_height; // 输出高度（像素）。
		float jpeg_quality; // 每帧 JPEG 编码质量。
		bool enable_timestamp_chunks; // 是否记录时间戳块。
		bool enable_repeat_frame_marking; // 是否标记因游戏卡顿生成的重复帧。

		RecordingConfig() :
				target_fps(30),
				video_width(1920),
				video_height(1080),
				jpeg_quality(0.85f),
				enable_timestamp_chunks(true),
				enable_repeat_frame_marking(true) {}
	};

	// 工作线程累计的视频统计。
	struct RecordingStats {
		uint32_t total_recorded_frames = 0; // 输出总帧数。
		uint32_t new_frames_count = 0; // 来自游戏的新画面数。
		uint32_t repeated_frames_count = 0; // 为连续时间轴补出的重复帧数。
		uint64_t recording_duration_us = 0; // 录制时长（微秒）。
		uint64_t avg_frame_process_time_us = 0; // 每帧平均编码耗时（微秒）。
		uint32_t last_game_frame_sequence = 0; // 最近消费的游戏帧序号。
	};

private:
	// 固定录制节拍。
	static const uint64_t FRAME_INTERVAL_USEC = 1000000 / 30; // 固定 30 fps 的约 33333 微秒节拍。

	// 工作线程控制。
	Thread recording_thread; // Godot 工作线程对象。
	std::atomic<bool> recording_active{ false }; // 线程循环退出条件。
	std::atomic<bool> thread_started{ false }; // 防止重复启动并指导析构等待。

	// 数据源与输出。
	ThreadSafeFrameBuffer *frame_buffer = nullptr; // 借用的主线程/录制线程交换缓冲。
	Ref<SimpleVideoWriter> video_writer; // 引用计数的 MJPEG AVI 写入器。

	// 当前录制配置。
	RecordingConfig config; // 当前输出参数。

	// 游戏帧与重复帧状态。
	uint32_t last_game_frame_sequence = 0; // 用于判断本次读取是否为新游戏帧。
	ThreadSafeFrameBuffer::FrameData last_valid_frame; // 缺帧时重复写入的最后有效画面。
	bool has_valid_frame = false; // last_valid_frame 是否可用。

	// 工作线程统计。
	RecordingStats stats; // 当前统计快照。
	uint64_t recording_start_time = 0; // OS 单调时钟起点（微秒）。
	uint64_t last_stats_update_time = 0; // 最近一次统计更新时间。

	// 统计同步保护。
	mutable Mutex stats_mutex; // 保护主线程读取与工作线程更新 stats。

	// 工作线程入口与循环。
	static void recording_thread_func(void *p_userdata);
	void recording_loop();

	// 单帧处理、统计和重复帧标记。
	bool process_frame(uint64_t current_recording_time);
	void update_statistics(uint64_t frame_process_start_time);
	uint8_t determine_frame_flags(const ThreadSafeFrameBuffer::FrameData &frame_data);

public:
	IndependentVideoRecorder();
	~IndependentVideoRecorder();

	// 直接调用方：ObsStyleMovieWriter::setup_components；只配置资源，不启动线程。
	Error initialize(ThreadSafeFrameBuffer *p_frame_buffer,
			const String &p_video_path,
			const RecordingConfig &p_config = RecordingConfig());

	// 直接调用方：ObsStyleMovieWriter 的录制启动流程。
	Error start_recording();

	// 停止、等待工作线程并关闭视频写入器；析构前必须完成。
	void stop_recording();

	// 线程安全的录制与线程状态查询。
	bool is_recording() const { return recording_active.load(); }
	bool is_thread_running() const { return thread_started.load(); }

	// 返回加锁复制的统计快照。
	RecordingStats get_statistics() const;

	// 返回重复帧占输出总帧数的比例。
	float get_repeat_frame_ratio() const;

	// 更新后续处理参数；调用方需避免与启动/停止并发。
	void update_config(const RecordingConfig &p_config);

	// 在 stats_mutex 保护下清零统计。
	void reset_statistics();

	// 生成面向日志的录制进度摘要。
	String get_debug_info() const;
};

#endif // SPX_INDEPENDENT_VIDEO_RECORDER_H
