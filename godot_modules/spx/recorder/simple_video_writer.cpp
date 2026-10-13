/**************************************************************************/
/*  simple_video_writer.cpp                                              */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#include "simple_video_writer.h"
#include "core/os/os.h"
#include "core/string/print_string.h"
#include "movie_utils.h"

// 最小化 MJPEG AVI 写入器：每个 Godot Image 独立编码为 JPEG，close 时补索引与帧数。
// 直接调用方：IndependentVideoRecorder；顶层调用方：ObsStyleMovieWriter 实时录制。
// 单个 FileAccess 实例由视频 worker 串行写入，停止并 join 后才允许 close。

SimpleVideoWriter::SimpleVideoWriter() {
	frame_count = 0;
	quality = 0.75f;
}

SimpleVideoWriter::~SimpleVideoWriter() {
	if (f.is_valid() && f->is_open()) {
		close();
	}
}

// 创建输出文件并写入占位 AVI/MJPEG 流头。
// 直接调用方：IndependentVideoRecorder::initialize()；顶层调用方：开始实时录制。
Error SimpleVideoWriter::open(const String &p_path, const Size2i &p_movie_size, uint32_t p_fps, float p_quality) {
	base_path = p_path;
	fps = p_fps;
	quality = p_quality;
	frame_count = 0;
	jpg_frame_sizes.clear();

	f = FileAccess::open(base_path, FileAccess::WRITE);
	ERR_FAIL_COND_V(f.is_null(), ERR_CANT_OPEN);

	// Write AVI header - video stream only
	// 写入仅含 MJPEG 视频流的 AVI 头；文件长度和帧数在 close() 回填。
	f->store_buffer((const uint8_t *)"RIFF", 4);
	f->store_32(0); // Total length (update later)
	f->store_buffer((const uint8_t *)"AVI ", 4);
	f->store_buffer((const uint8_t *)"LIST", 4);
	f->store_32(196); // hdrl size: 4 + 4 + 4 + 56 + 4 + 4 + 132 (video stream only)
	f->store_buffer((const uint8_t *)"hdrl", 4);
	f->store_buffer((const uint8_t *)"avih", 4);
	f->store_32(56);

	f->store_32(1000000 / p_fps); // Microsecs per frame
	f->store_32(7000); // Max bytes per second
	f->store_32(0); // Padding Granularity
	f->store_32(16);
	total_frames_ofs = f->get_position();
	f->store_32(0); // Total frames (update later)
	f->store_32(0); // Initial frames
	f->store_32(1); // Streams (only 1 video stream)
	f->store_32(0); // Suggested buffer size
	f->store_32(p_movie_size.width); // Movie Width
	f->store_32(p_movie_size.height); // Movie Height
	for (uint32_t i = 0; i < 4; i++) {
		f->store_32(0); // Reserved
	}

	// Video stream header
	// strh/strf 描述 MJPG 编码、分辨率与固定 FPS。
	f->store_buffer((const uint8_t *)"LIST", 4);
	f->store_32(132); // strl size: 4 + 4 + 4 + 48 + 4 + 4 + 40 + 4 + 4 + 16
	f->store_buffer((const uint8_t *)"strl", 4);
	f->store_buffer((const uint8_t *)"strh", 4);
	f->store_32(48);
	f->store_buffer((const uint8_t *)"vids", 4);
	f->store_buffer((const uint8_t *)"MJPG", 4);
	f->store_32(0); // Flags
	f->store_16(0); // Priority
	f->store_16(0); // Language
	f->store_32(0); // Initial Frames
	f->store_32(1); // Scale
	f->store_32(p_fps); // FPS
	f->store_32(0); // Start
	total_frames_ofs2 = f->get_position();
	f->store_32(0); // Number of frames (to be updated later)
	f->store_32(0); // Suggested Buffer Size
	f->store_32(0); // Quality
	f->store_32(0); // Sample Size

	f->store_buffer((const uint8_t *)"strf", 4);
	f->store_32(40); // Size
	f->store_32(40); // Size

	f->store_32(p_movie_size.width); // Width
	f->store_32(p_movie_size.height); // Height
	f->store_16(1); // Planes
	f->store_16(24); // Bitcount
	f->store_buffer((const uint8_t *)"MJPG", 4); // Compression

	f->store_32(((p_movie_size.width * 24 / 8 + 3) & 0xFFFFFFFC) * p_movie_size.height); // SizeImage
	f->store_32(0); // XPelsXMeter
	f->store_32(0); // YPelsXMeter
	f->store_32(0); // ClrUsed
	f->store_32(0); // ClrImportant

	f->store_buffer((const uint8_t *)"LIST", 4);
	f->store_32(16);

	f->store_buffer((const uint8_t *)"odml", 4);
	f->store_buffer((const uint8_t *)"dmlh", 4);
	f->store_32(4); // sizes

	total_frames_ofs3 = f->get_position();
	f->store_32(0); // Number of frames (to be updated later)

	// movi part starts
	// 后续 JPEG 帧从 movi 数据区开始顺序追加。
	f->store_buffer((const uint8_t *)"LIST", 4);
	movi_data_ofs = f->get_position();
	f->store_32(0); // Number of frames (to be updated later)
	f->store_buffer((const uint8_t *)"movi", 4);

	if (MovieDebugUtils::is_stdout_verbose()) {
		print_line(String("SimpleVideoWriter: Starting video recording to ") + base_path);
	}

	return OK;
}

// 复制并转换 Image 后编码单帧 JPEG。
// 直接调用方：IndependentVideoRecorder::process_frame()；顶层调用方：视频录制 worker。
// 复制可避免后台线程修改主线程发布的原始 Image 格式。
Error SimpleVideoWriter::write_frame(const Ref<Image> &p_image) {
	ERR_FAIL_COND_V(f.is_null(), ERR_UNCONFIGURED);
	ERR_FAIL_COND_V(p_image.is_null(), ERR_INVALID_PARAMETER);

	// Convert image to JPEG
	// JPEG 编码要求 RGB8；只转换 duplicate，不修改共享输入图像。
	Ref<Image> img = p_image->duplicate();
	if (img->get_format() != Image::FORMAT_RGB8) {
		img->convert(Image::FORMAT_RGB8);
	}

	PackedByteArray jpg_buffer = img->save_jpg_to_buffer(quality);
	uint32_t s = jpg_buffer.size();

	f->store_buffer((const uint8_t *)"00db", 4); // Stream 0, Video
	f->store_32(jpg_buffer.size()); // sizes
	f->store_buffer(jpg_buffer.ptr(), jpg_buffer.size());
	if (jpg_buffer.size() & 1) {
		f->store_8(0);
		s++;
	}
	jpg_frame_sizes.push_back(s);

	frame_count++;

	return OK;
}

// 写入视频索引、回填 RIFF/movi/帧数并释放 FileAccess Ref。
// 直接调用方：IndependentVideoRecorder::stop_recording()/析构；顶层调用方：录制停止流程。
void SimpleVideoWriter::close() {
	if (!f.is_valid() || !f->is_open()) {
		return;
	}

	// Write index - only contains video frames
	// idx1 只记录 00db 视频帧。
	f->store_buffer((const uint8_t *)"idx1", 4);
	f->store_32(4 * 4 * frame_count); // 16 bytes per index entry
	uint32_t ofs = 4;
	uint32_t all_data_size = 0;

	for (uint32_t i = 0; i < frame_count; i++) {
		f->store_buffer((const uint8_t *)"00db", 4);
		f->store_32(16); // AVI_KEYFRAME
		f->store_32(ofs);
		f->store_32(jpg_frame_sizes[i]);

		ofs += jpg_frame_sizes[i] + 8;
		all_data_size += jpg_frame_sizes[i];
	}

	// Update size information in the file header
	// seek 回三个帧数字段及文件长度/movi 大小占位位置。
	uint32_t file_size = f->get_position();
	f->seek(4);
	f->store_32(file_size - 8);
	f->seek(total_frames_ofs);
	f->store_32(frame_count);
	f->seek(total_frames_ofs2);
	f->store_32(frame_count);
	f->seek(total_frames_ofs3);
	f->store_32(frame_count);
	f->seek(movi_data_ofs);
	f->store_32(all_data_size + 4 + 16 * frame_count);

	f.unref();

	if (MovieDebugUtils::is_stdout_verbose()) {
		print_line(String("SimpleVideoWriter: Video recording completed, total frames: ") + String::num_int64(frame_count));
	}
}
