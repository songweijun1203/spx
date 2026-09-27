/**************************************************************************/
/*  independent_audio_recorder.h                                         */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#ifndef SPX_INDEPENDENT_AUDIO_RECORDER_H
#define SPX_INDEPENDENT_AUDIO_RECORDER_H

#include "core/os/mutex.h"
#include "core/os/os.h"
#include "core/os/thread.h"
#include "core/templates/ring_buffer.h"
#include "simple_audio_writer.h"
#include <atomic>

// 前置声明，避免录制器与捕获驱动头文件互相包含。
class HybridAudioDriver;

// 固定采样节拍的独立音频录制器，从 HybridAudioDriver 接收 Master 混音并写入 AVI 音轨。
// 直接调用方：ObsStyleMovieWriter；数据生产方：HybridAudioDriver 的音频线程。
// 顶层调用方：Go 录制 API 或 Godot 命令行电影录制。
// Godot 规则：Thread 退出前必须 wait_to_finish；RefCounted 写入器由 Ref 跨线程保活，
// 所有同时被主线程和录音线程访问的数据必须通过原子量或 Mutex 同步。
class IndependentAudioRecorder {
public:
	// 音频录制配置，由 ObsStyleMovieWriter 在启动线程前一次性传入。
	struct AudioConfig {
		uint32_t sample_rate; // 每秒采样帧数。
		uint32_t channels; // 交错 PCM 通道数。
		uint32_t chunk_size; // 每批处理的采样帧数，48 kHz 时默认对应 10 ms。
		uint32_t buffer_size_seconds; // 环形缓冲容量（秒）。
		bool enable_audio_monitoring; // 是否输出额外监听/诊断信息。

		AudioConfig() :
				sample_rate(48000),
				channels(2),
				chunk_size(480),
				buffer_size_seconds(2),
				enable_audio_monitoring(false) {}
	};

	// 音频线程累计统计；外部读取时由 stats_mutex 保护。
	struct AudioStats {
		uint64_t total_chunks_recorded = 0; // 已写入的数据块数。
		uint64_t total_samples_recorded = 0; // 已写入的跨通道样本总数。
		uint64_t buffer_overruns = 0; // 生产快于消费导致的溢出次数。
		uint64_t buffer_underruns = 0; // 固定节拍取数不足的次数。
		uint64_t recording_duration_us = 0; // 已录制时长（微秒）。
		uint32_t current_buffer_level = 0; // 缓冲占用百分比（0-100）。
		uint64_t avg_chunk_process_time_us = 0; // 每块平均处理耗时（微秒）。
	};

private:
	static const uint64_t CHUNK_INTERVAL_USEC = 10000; // 独立线程固定 10 ms 唤醒间隔。

	Thread recording_thread; // Godot 工作线程对象。
	std::atomic<bool> recording_active{ false }; // 线程循环退出条件。
	std::atomic<bool> thread_started{ false }; // 防止重复启动并指导析构等待。

	// 音频参数。
	AudioConfig config; // 当前录制参数，运行中更新需由调用方保证时序。

	// 数据源与输出。
	HybridAudioDriver *audio_driver = nullptr; // 借用的数据源；MovieWriter 保证其生命周期更长。
	Ref<SimpleAudioWriter> audio_writer; // 引用计数的 AVI PCM 写入器。

	// 音频环形缓冲。
	RingBuffer<int32_t> audio_ring_buffer; // 音频回调与录制线程之间的 PCM 队列。
	mutable Mutex buffer_mutex; // 保护 RingBuffer 的读写操作。
	std::atomic<uint32_t> buffer_read_pos{ 0 }; // 调试/统计用累计读位置。
	std::atomic<uint32_t> buffer_write_pos{ 0 }; // 调试/统计用累计写位置。
	uint32_t buffer_size = 0; // 缓冲容量（跨通道 int32 样本数）。

	// 工作线程复用的临时缓冲。
	Vector<int32_t> temp_audio_buffer; // 接收捕获数据的临时空间。
	Vector<int32_t> chunk_buffer; // 按固定块大小提交给写入器的空间。

	// 跨线程统计。
	AudioStats stats; // 当前统计快照。
	uint64_t recording_start_time = 0; // OS 单调时钟起点（微秒）。
	mutable Mutex stats_mutex; // 保护 stats 的跨线程读写。

	// 工作线程入口与循环。
	static void recording_thread_func(void *p_userdata);
	void recording_loop();

	// 单块处理与统计更新。
	bool process_audio_chunk(uint64_t current_recording_time);
	void update_statistics(uint64_t chunk_process_start_time, uint32_t samples_processed);
	void update_buffer_level();

	// 缓冲读取及欠载/过载处理。
	bool read_audio_chunk(Vector<int32_t> &output_buffer, uint32_t requested_samples);
	void handle_buffer_underrun();
	void handle_buffer_overrun();

public:
	IndependentAudioRecorder();
	~IndependentAudioRecorder();

	// 直接调用方：ObsStyleMovieWriter::setup_components；只完成资源配置，不启动线程。
	Error initialize(HybridAudioDriver *p_audio_driver,
			const String &p_audio_path,
			const AudioConfig &p_config = AudioConfig());

	// 直接调用方：ObsStyleMovieWriter::write_begin/begin_realtime。
	Error start_recording();

	// 停止、等待工作线程并关闭写入器；析构前必须完成。
	void stop_recording();

	// 线程安全的录制与线程状态查询。
	bool is_recording() const { return recording_active.load(); }
	bool is_thread_running() const { return thread_started.load(); }

	// 外部异常清理时只关闭循环标志，真正回收仍由 stop_recording 完成。
	void mark_inactive() { recording_active.store(false); }

	// 直接调用方：HybridAudioDriver 音频回调；此处不能执行阻塞文件 I/O。
	void on_audio_output(const int32_t *p_buffer, int p_frame_count);

	// 返回加锁复制的统计快照。
	AudioStats get_statistics() const;

	// 录音缓冲状态查询。
	uint32_t get_available_samples() const;
	bool has_audio_data() const;
	float get_buffer_usage_ratio() const;

	// 更新后续处理参数；调用方需避免与启动/停止并发。
	void update_config(const AudioConfig &p_config);

	// 在 stats_mutex 保护下清零统计。
	void reset_statistics();

	// 生成面向日志的状态摘要。
	String get_debug_info() const;

	// 返回内部配置引用，只应在录制器生命周期内读取。
	const AudioConfig &get_config() const { return config; }
};

#endif // SPX_INDEPENDENT_AUDIO_RECORDER_H
