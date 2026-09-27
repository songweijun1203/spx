/**************************************************************************/
/*  thread_safe_frame_buffer.cpp                                         */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#include "thread_safe_frame_buffer.h"
#include "core/os/os.h"

// 单生产者（Godot 主线程）、单消费者（视频录制线程）的双缓冲交换实现。
// Image 通过 Ref 保持资源生命周期；只交换完整 FrameData，不让 worker 读取正在写入的槽位。

ThreadSafeFrameBuffer::ThreadSafeFrameBuffer() {
	has_new_data.store(false);
	last_sequence.store(0);
	total_updates.store(0);
	buffer_switches.store(0);
}

ThreadSafeFrameBuffer::~ThreadSafeFrameBuffer() {
	reset();
}

// 发布一张完整画面快照。
// 直接调用方：ObsStyleMovieWriter::write_frame()；顶层调用方：Godot MovieWriter/主循环逐帧回调。
// 调用约束：只允许一个生产线程；Image 在发布后不得被生产者原地修改。
void ThreadSafeFrameBuffer::update_frame(const Ref<Image> &new_frame, uint64_t timestamp, uint32_t sequence) {
	if (new_frame.is_null()) {
		return;
	}

	total_updates.fetch_add(1);

	// Fast path: get a reference to the write buffer without locking
	// 单生产者先取得当前写槽，写槽此时不会被消费者选中。
	FrameData *write_buffer = writing_to_a ? &buffer_a : &buffer_b;

	// Update write buffer (non-critical section)
	// 在锁外填充可缩短消费者等待时间。
	write_buffer->image = new_frame;
	write_buffer->game_timestamp = timestamp;
	write_buffer->frame_sequence = sequence;
	write_buffer->is_new_frame = true;

	// Critical section: switch buffer pointers
	// 只在临界区翻转角色，使消费者一次看到完整发布结果。
	{
		MutexLock lock(buffer_mutex);
		writing_to_a = !writing_to_a;
		buffer_switches.fetch_add(1);
	}

	has_new_data.store(true);
	last_sequence.store(sequence);
}

// 复制当前稳定读槽。
// 直接调用方：IndependentVideoRecorder::process_frame()；顶层调用方：视频录制线程。
ThreadSafeFrameBuffer::FrameData ThreadSafeFrameBuffer::get_current_frame() const {
	MutexLock lock(buffer_mutex);

	// Get the stable read buffer (not the write buffer)
	// 始终选择写槽的另一侧。
	const FrameData *read_buffer = writing_to_a ? &buffer_b : &buffer_a;

	// Copy data (within the lock to ensure consistency)
	// 在锁内复制元数据和 Ref，确保序号、时间戳与图像属于同一次发布。
	FrameData result = *read_buffer;

	return result;
}

// 清空两个槽位和统计；直接调用方：ObsStyleMovieWriter 清理/本类析构。
// 调用前必须先停止视频消费者线程，避免 reset 与 get_current_frame 并发改变会话语义。
void ThreadSafeFrameBuffer::reset() {
	MutexLock lock(buffer_mutex);

	buffer_a = FrameData();
	buffer_b = FrameData();
	writing_to_a = true;

	has_new_data.store(false);
	last_sequence.store(0);
	total_updates.store(0);
	buffer_switches.store(0);
}
