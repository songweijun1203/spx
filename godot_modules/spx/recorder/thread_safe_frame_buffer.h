/**************************************************************************/
/*  thread_safe_frame_buffer.h                                           */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#ifndef SPX_THREAD_SAFE_FRAME_BUFFER_H
#define SPX_THREAD_SAFE_FRAME_BUFFER_H

#include "core/io/image.h"
#include "core/os/mutex.h"
#include "core/os/os.h"
#include <atomic>

// 游戏主线程与独立视频线程之间的双缓冲交换器。
// 直接调用方：ObsStyleMovieWriter::add_realtime_frame（写）和 IndependentVideoRecorder（读）；
// 顶层调用方：Godot 每帧回调或 Go 手动录制流程。
// Godot 规则：Ref<Image> 的引用计数负责图像保活，但缓冲槽切换仍必须由 Mutex 保证一致快照。
class ThreadSafeFrameBuffer {
public:
	struct FrameData {
		Ref<Image> image; // 引用计数的画面快照。
		uint64_t game_timestamp; // 游戏提交时间（微秒）。
		uint32_t frame_sequence; // 游戏帧单调序号。
		bool is_new_frame = false; // 相对上次读取是否为新画面。

		FrameData() :
				game_timestamp(0), frame_sequence(0), is_new_frame(false) {}

		FrameData(const FrameData &other) :
				image(other.image), game_timestamp(other.game_timestamp), frame_sequence(other.frame_sequence), is_new_frame(other.is_new_frame) {}

		FrameData &operator=(const FrameData &other) {
			if (this != &other) {
				image = other.image;
				game_timestamp = other.game_timestamp;
				frame_sequence = other.frame_sequence;
				is_new_frame = other.is_new_frame;
			}
			return *this;
		}
	};

private:
	FrameData buffer_a; // 双缓冲槽 A。
	FrameData buffer_b; // 双缓冲槽 B。
	bool writing_to_a = true; // 主线程当前写入槽；另一槽供录制线程稳定读取。

	// 跨线程同步状态。
	mutable Mutex buffer_mutex; // 保护写入、槽切换和一致快照复制。
	std::atomic<bool> has_new_data{ false }; // 是否存在尚未消费的新帧。
	std::atomic<uint32_t> last_sequence{ 0 }; // 最近消费的序号。

	// 无锁读取的累计统计。
	std::atomic<uint64_t> total_updates{ 0 }; // 主线程提交次数。
	std::atomic<uint64_t> buffer_switches{ 0 }; // 成功切换稳定槽次数。

public:
	ThreadSafeFrameBuffer();
	~ThreadSafeFrameBuffer();

	// 仅由游戏主线程提交画面；new_frame 通过 Ref 在录制线程读取期间保活。
	void update_frame(const Ref<Image> &new_frame, uint64_t timestamp, uint32_t sequence);

	// 仅由录制线程取得稳定快照，返回值不暴露内部缓冲槽引用。
	FrameData get_current_frame() const;

	// 是否存在尚未被录制线程消费的新画面。
	bool has_new_frame() const { return has_new_data.load(); }

	// 最近一次稳定读取的游戏帧序号。
	uint32_t get_last_sequence() const { return last_sequence.load(); }

	// 双缓冲累计统计。
	uint64_t get_total_updates() const { return total_updates.load(); }
	uint64_t get_buffer_switches() const { return buffer_switches.load(); }

	// 清空两个槽及原子状态；只能在录制线程停止后调用。
	void reset();
};

#endif // SPX_THREAD_SAFE_FRAME_BUFFER_H
