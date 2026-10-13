/**************************************************************************/
/*  independent_audio_recorder.cpp                                       */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#include "independent_audio_recorder.h"
#include "audio_driver_hybrid.h"
#include "core/os/os.h"
#include "core/string/print_string.h"
#include "movie_utils.h"

// 独立音频录制流水线：Godot 音频线程只把 PCM 推入环形缓冲，专用 Thread 按固定
// chunk 时钟消费并写文件，避免磁盘 I/O 阻塞 AudioEffect::process。

IndependentAudioRecorder::IndependentAudioRecorder() {
	recording_active.store(false);
	thread_started.store(false);
	buffer_read_pos.store(0);
	buffer_write_pos.store(0);
	buffer_size = 0;
	recording_start_time = 0;
}

IndependentAudioRecorder::~IndependentAudioRecorder() {
	if (is_recording()) {
		stop_recording();
	}
}

// 配置缓冲并打开仅音频 AVI writer，尚不启动工作线程。
// 直接调用方：ObsStyleMovieWriter::setup_components()；顶层调用方：Go/命令行开始录制。
// FileAccess 实例会在录制线程写入；录制期间不得从其他线程同时操作该 writer。
Error IndependentAudioRecorder::initialize(HybridAudioDriver *p_audio_driver,
		const String &p_audio_path,
		const AudioConfig &p_config) {
	if (!p_audio_driver || p_audio_path.is_empty()) {
		ERR_PRINT("IndependentAudioRecorder: Invalid audio_driver or audio_path");
		return ERR_INVALID_PARAMETER;
	}

	audio_driver = p_audio_driver;
	config = p_config;

	audio_writer.instantiate();

	Error open_result = audio_writer->open(p_audio_path, config.sample_rate, config.channels);
	if (open_result != OK) {
		ERR_PRINT("IndependentAudioRecorder: Failed to open audio writer");
		return open_result;
	}

	buffer_size = config.sample_rate * config.channels * config.buffer_size_seconds;

	// Godot RingBuffer 用 2 的幂表示容量，因此向上取整配置的样本数。
	int power = 0;
	while ((1 << power) < (int)buffer_size) {
		power++;
	}
	int actual_buffer_size = 1 << power; // 实际可用容量。

	audio_ring_buffer = RingBuffer<int32_t>(power);
	buffer_size = actual_buffer_size; // 保存取整后的实际容量。

	temp_audio_buffer.resize(config.chunk_size * config.channels);
	chunk_buffer.resize(config.chunk_size * config.channels);

	reset_statistics();

	if (MovieDebugUtils::is_stdout_verbose()) {
		print_line("IndependentAudioRecorder initialization completed");
		print_line(String("Audio path: ") + p_audio_path);
		print_line(String("Sample rate: ") + String::num_int64(config.sample_rate) + "Hz");
		print_line(String("Channels: ") + String::num_int64(config.channels));
		print_line(String("Chunk size: ") + String::num_int64(config.chunk_size) + " samples");
		print_line(String("Buffer size: ") + String::num_int64(buffer_size) + " samples (" +
				String::num_real((float)config.buffer_size_seconds) + " seconds)");
	}

	return OK;
}

// 清空旧数据并启动 Godot Thread。
// 直接调用方：ObsStyleMovieWriter::write_begin()；顶层调用方：实时录制启动。
// Godot Thread 规则：对象销毁前必须先令 recording_active=false 并 wait_to_finish()。
Error IndependentAudioRecorder::start_recording() {
	if (recording_active.load()) {
		ERR_PRINT("IndependentAudioRecorder: Recording is already in progress");
		return ERR_ALREADY_IN_USE;
	}

	if (!audio_driver || audio_writer.is_null()) {
		ERR_PRINT("IndependentAudioRecorder: Not initialized");
		return ERR_UNCONFIGURED;
	}

	audio_ring_buffer.clear();
	buffer_read_pos.store(0);
	buffer_write_pos.store(0);

	recording_active.store(true);
	recording_start_time = OS::get_singleton()->get_ticks_usec();

	recording_thread.start(recording_thread_func, this);
	thread_started.store(true);

	if (MovieDebugUtils::is_stdout_verbose()) {
		print_line("IndependentAudioRecorder: Start recording");
	}

	return OK;
}

// 原子地只执行一次停止：先通知 worker 退出并 join，再关闭/回填 AVI 文件头。
// 直接调用方：ObsStyleMovieWriter::write_end()/cleanup_components()/析构；顶层调用方：停止录制。
void IndependentAudioRecorder::stop_recording() {
	// Atomic check-and-set to prevent double cleanup
	// 原子 compare-exchange 防止并发或重复停止造成二次 join/close。
	bool expected = true;
	if (!recording_active.compare_exchange_strong(expected, false)) {
		// 已停止或另一调用方正在停止。
		return;
	}

	if (thread_started.load()) {
		recording_thread.wait_to_finish();
		thread_started.store(false);
	}

	if (audio_writer.is_valid()) {
		audio_writer->close();
	}

	if (MovieDebugUtils::is_stdout_verbose()) {
		AudioStats final_stats = get_statistics();
		print_line(String("Audio recording completed - Total chunks: ") + String::num_int64(final_stats.total_chunks_recorded));
		print_line(String("Total samples: ") + String::num_int64(final_stats.total_samples_recorded));
		print_line(String("Recording duration: ") + String::num_real(final_stats.recording_duration_us / 1000000.0) + " seconds");
		print_line(String("Buffer overruns: ") + String::num_int64(final_stats.buffer_overruns));
		print_line(String("Buffer underruns: ") + String::num_int64(final_stats.buffer_underruns));
	}
}

// 音频线程生产入口，只在互斥区内写环形缓冲，不执行文件 I/O。
// 直接调用方：HybridAudioDriver::capture_audio_data()；顶层调用方：Godot AudioEffect 混音回调。
// Godot 规则：音频线程不能等待录制 worker 或访问 SceneTree；缓冲已满时本批直接丢弃。
void IndependentAudioRecorder::on_audio_output(const int32_t *p_buffer, int p_frame_count) {
	if (!recording_active.load() || !p_buffer || p_frame_count <= 0) {
		return;
	}

	MutexLock lock(buffer_mutex);

	uint32_t samples_to_write = p_frame_count * config.channels;

	int available_space = audio_ring_buffer.space_left();
	if ((int)samples_to_write > available_space) {
		handle_buffer_overrun();
		return;
	}

	audio_ring_buffer.write(p_buffer, samples_to_write);
}

uint32_t IndependentAudioRecorder::get_available_samples() const {
	return audio_ring_buffer.data_left();
}

bool IndependentAudioRecorder::has_audio_data() const {
	return get_available_samples() >= (config.chunk_size * config.channels);
}

float IndependentAudioRecorder::get_buffer_usage_ratio() const {
	if (buffer_size == 0) {
		return 0.0f;
	}
	return (float)get_available_samples() / (float)buffer_size;
}

IndependentAudioRecorder::AudioStats IndependentAudioRecorder::get_statistics() const {
	MutexLock lock(stats_mutex);
	return stats;
}

void IndependentAudioRecorder::update_config(const AudioConfig &p_config) {
	if (is_recording()) {
		ERR_PRINT("IndependentAudioRecorder: Cannot update configuration while recording");
		return;
	}

	config.sample_rate = p_config.sample_rate;
	config.channels = p_config.channels;
	config.chunk_size = p_config.chunk_size;
	config.buffer_size_seconds = p_config.buffer_size_seconds;
	config.enable_audio_monitoring = p_config.enable_audio_monitoring;

	// 重新计算并向上取整 RingBuffer 容量；仅允许停止状态调用。
	buffer_size = config.sample_rate * config.channels * config.buffer_size_seconds;

	// Godot RingBuffer 的构造参数是容量指数。
	int power = 0;
	while ((1 << power) < (int)buffer_size) {
		power++;
	}
	int actual_buffer_size = 1 << power; // 实际容量。

	// 重新建立环形缓冲；旧数据全部丢弃。
	audio_ring_buffer = RingBuffer<int32_t>(power);
	buffer_size = actual_buffer_size; // 保存实际容量。

	temp_audio_buffer.resize(config.chunk_size * config.channels);
	chunk_buffer.resize(config.chunk_size * config.channels);
}

void IndependentAudioRecorder::reset_statistics() {
	MutexLock lock(stats_mutex);

	stats = AudioStats();
	buffer_read_pos.store(0);
	buffer_write_pos.store(0);
	recording_start_time = 0;
}

String IndependentAudioRecorder::get_debug_info() const {
	AudioStats current_stats = get_statistics();

	String info;
	info += "=== IndependentAudioRecorder Debug Info ===\n";
	info += String("Recording status: ") + (is_recording() ? "Running" : "Stopped") + "\n";
	info += String("Thread status: ") + (is_thread_running() ? "Running" : "Stopped") + "\n";
	info += String("Total audio chunks: ") + String::num_int64(current_stats.total_chunks_recorded) + "\n";
	info += String("Total samples: ") + String::num_int64(current_stats.total_samples_recorded) + "\n";
	info += String("Recording duration: ") + String::num_real(current_stats.recording_duration_us / 1000000.0) + " seconds\n";
	info += String("Buffer usage: ") + String::num_int64(current_stats.current_buffer_level) + "%\n";
	info += String("Available samples: ") + String::num_int64(get_available_samples()) + "\n";
	info += String("Buffer overruns: ") + String::num_int64(current_stats.buffer_overruns) + "\n";
	info += String("Buffer underruns: ") + String::num_int64(current_stats.buffer_underruns) + "\n";
	info += String("Avg chunk process time: ") + String::num_int64(current_stats.avg_chunk_process_time_us) + " microseconds\n";
	info += String("Config sample rate: ") + String::num_int64(config.sample_rate) + "Hz\n";
	info += String("Config channels: ") + String::num_int64(config.channels) + "\n";
	info += String("Config chunk size: ") + String::num_int64(config.chunk_size) + " samples\n";
	info += "==========================================";

	return info;
}

// Godot Thread 的 C 风格 trampoline。
// 直接调用方：Thread::start()；顶层调用方：start_recording()。
void IndependentAudioRecorder::recording_thread_func(void *p_userdata) {
	IndependentAudioRecorder *recorder = static_cast<IndependentAudioRecorder *>(p_userdata);
	recorder->recording_loop();
}

// 独立音频线程主循环：按 CHUNK_INTERVAL_USEC 的单调时钟节奏消费并写入 PCM chunk。
// 直接调用方：recording_thread_func()；顶层调用方：start_recording()。
// 本线程只操作受锁缓冲、统计和专属 FileAccess，不触碰 Godot SceneTree/渲染对象。
void IndependentAudioRecorder::recording_loop() {
	uint64_t next_chunk_time = recording_start_time;
	uint64_t chunk_count = 0;

	while (recording_active.load()) {
		uint64_t current_time = OS::get_singleton()->get_ticks_usec();

		if (current_time >= next_chunk_time) {
			uint64_t chunk_process_start = OS::get_singleton()->get_ticks_usec();

			bool chunk_processed = process_audio_chunk(next_chunk_time - recording_start_time);

			if (chunk_processed) {
				chunk_count++;
				update_statistics(chunk_process_start, config.chunk_size);

				if (MovieDebugUtils::is_stdout_verbose() && chunk_count % 1000 == 0) {
					AudioStats current_stats = get_statistics();
					print_line(String("Audio recording progress: ") + String::num_int64(chunk_count) + " chunks, " +
							String("Buffer usage: ") + String::num_int64(current_stats.current_buffer_level) + "%");
				}
			}

			next_chunk_time += CHUNK_INTERVAL_USEC;
		}

		// 精确休眠：粗睡眠预留 500 微秒，减少跨过下一个 chunk 截止点的概率。
		current_time = OS::get_singleton()->get_ticks_usec();
		if (next_chunk_time > current_time) {
			uint64_t sleep_time = next_chunk_time - current_time;
			if (sleep_time > 1000) { // 等待超过 1ms 才进入系统睡眠。
				OS::get_singleton()->delay_usec(sleep_time - 500); // 预留 500 微秒余量。
			}
		}
	}
}

// 从环形缓冲取一个完整 chunk 并写入独占的 SimpleAudioWriter。
// 直接调用方：recording_loop()；顶层调用方：音频录制 worker。
bool IndependentAudioRecorder::process_audio_chunk(uint64_t current_recording_time) {
	if (!read_audio_chunk(chunk_buffer, config.chunk_size * config.channels)) {
		handle_buffer_underrun();
		return false;
	}
	Error write_result = audio_writer->write_audio_chunk(
			chunk_buffer.ptr(),
			config.chunk_size);

	if (write_result != OK) {
		ERR_PRINT("IndependentAudioRecorder: Failed to write audio chunk");
		return false;
	}
	{
		MutexLock lock(stats_mutex);
		stats.total_chunks_recorded++;
		stats.total_samples_recorded += config.chunk_size;
	}

	return true;
}

bool IndependentAudioRecorder::read_audio_chunk(Vector<int32_t> &output_buffer, uint32_t requested_samples) {
	uint32_t available = get_available_samples();

	if (available < requested_samples) {
		return false; // 数据不足，留给下一轮并记录 underrun。
	}

	MutexLock lock(buffer_mutex);

	int samples_read = audio_ring_buffer.read(output_buffer.ptrw(), requested_samples);

	if (samples_read < (int)requested_samples) {
		// 并发消费导致少读时用静音补齐，保证 writer 收到固定 chunk 大小。
		for (int i = samples_read; i < (int)requested_samples; i++) {
			output_buffer.write[i] = 0;
		}
	}

	return true;
}

void IndependentAudioRecorder::update_statistics(uint64_t chunk_process_start_time, uint32_t samples_processed) {
	uint64_t current_time = OS::get_singleton()->get_ticks_usec();
	uint64_t process_time = current_time - chunk_process_start_time;

	MutexLock lock(stats_mutex);

	stats.recording_duration_us = current_time - recording_start_time;

	// 用移动平均平滑单次磁盘写入抖动。
	if (stats.avg_chunk_process_time_us == 0) {
		stats.avg_chunk_process_time_us = process_time;
	} else {
		// 旧值权重 90%，新值权重 10%。
		stats.avg_chunk_process_time_us = (stats.avg_chunk_process_time_us * 9 + process_time) / 10;
	}

	update_buffer_level();
}

void IndependentAudioRecorder::update_buffer_level() {
	uint32_t available = get_available_samples();
	stats.current_buffer_level = (available * 100) / buffer_size;
}

void IndependentAudioRecorder::handle_buffer_underrun() {
	MutexLock lock(stats_mutex);
	stats.buffer_underruns++;

	// 预先清零 chunk；当前轮不写文件，后续成功读取会覆盖该缓冲。
	chunk_buffer.fill(0);
}

void IndependentAudioRecorder::handle_buffer_overrun() {
	MutexLock lock(stats_mutex);
	stats.buffer_overruns++;

	// 兼容统计游标前移；当前 on_audio_output 会放弃整批新数据，不修改 RingBuffer 内容。
	uint32_t samples_to_skip = config.chunk_size * config.channels;
	uint32_t read_pos = buffer_read_pos.load();
	read_pos = (read_pos + samples_to_skip) % buffer_size;
	buffer_read_pos.store(read_pos);
}
