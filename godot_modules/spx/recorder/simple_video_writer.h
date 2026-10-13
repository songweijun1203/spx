/**************************************************************************/
/*  simple_video_writer.h                                                */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#ifndef SPX_SIMPLE_VIDEO_WRITER_H
#define SPX_SIMPLE_VIDEO_WRITER_H

#include "core/io/file_access.h"
#include "core/io/image.h"
#include "core/object/ref_counted.h"

// 最小 MJPEG AVI 写入器，将每个 Image 编码为 JPEG 块并在 close 时回填 AVI 索引。
// 直接调用方：IndependentVideoRecorder；顶层调用方：ObsStyleMovieWriter 录制会话。
// Godot 规则：Image/FileAccess 均由 Ref 保活，编码和写盘仅在视频工作线程执行。
class SimpleVideoWriter : public RefCounted {
	GDCLASS(SimpleVideoWriter, RefCounted);

private:
	Ref<FileAccess> f; // 当前输出文件句柄。
	String base_path; // 输出路径，用于日志和错误信息。

	uint32_t fps; // AVI 视频流时间基准帧率。
	uint32_t frame_count = 0; // 已写入 JPEG 帧数。
	float quality = 0.75f; // Godot JPEG 编码质量。

	uint64_t total_frames_ofs; // 主 AVI 头总帧数字段偏移。
	uint64_t total_frames_ofs2; // 视频流头长度字段偏移。
	uint64_t total_frames_ofs3; // OpenDML/兼容头帧数字段偏移。
	uint64_t movi_data_ofs; // AVI movi 数据区起始偏移。

	Vector<uint32_t> jpg_frame_sizes; // 用于 close 时生成 idx1 的各 JPEG 块长度。

public:
	SimpleVideoWriter();
	~SimpleVideoWriter();

	Error open(const String &p_path, const Size2i &p_movie_size, uint32_t p_fps, float p_quality = 0.75f);
	Error write_frame(const Ref<Image> &p_image);
	void close();

	void set_quality(float p_quality) { quality = p_quality; }
	uint32_t get_frame_count() const { return frame_count; }
};

#endif // SPX_SIMPLE_VIDEO_WRITER_H
