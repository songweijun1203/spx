/**************************************************************************/
/*  simple_audio_writer.h                                                */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#ifndef SPX_SIMPLE_AUDIO_WRITER_H
#define SPX_SIMPLE_AUDIO_WRITER_H

#include "core/io/file_access.h"
#include "core/object/ref_counted.h"

// 最小 PCM AVI 写入器，负责音频中间文件并在 close 时回填头部长度和索引。
// 直接调用方：IndependentAudioRecorder；顶层调用方：ObsStyleMovieWriter 录制会话。
// Godot 规则：RefCounted 必须由 Ref 持有；FileAccess 失败以空 Ref/Error 表示。
class SimpleAudioWriter : public RefCounted {
	GDCLASS(SimpleAudioWriter, RefCounted);

private:
	Ref<FileAccess> f; // 当前输出文件句柄。
	String base_path; // 输出路径，用于日志和错误信息。

	uint32_t mix_rate; // PCM 采样率。
	uint32_t channels; // PCM 通道数。
	uint32_t audio_chunk_count = 0; // 已写入 AVI 音频块数。
	uint32_t audio_block_size; // 单采样帧字节数。

	uint64_t total_audio_frames_ofs; // close 时回填总采样帧数的位置。
	uint64_t movi_data_ofs; // AVI movi 数据区起始偏移。

	Vector<uint32_t> audio_chunk_sizes; // 用于 close 时生成 idx1 的各块长度。

public:
	SimpleAudioWriter();
	~SimpleAudioWriter();

	Error open(const String &p_path, uint32_t p_sample_rate, uint32_t p_channels);
	Error write_audio_chunk(const int32_t *p_audio_data, int p_frame_count);
	void close();

	uint32_t get_chunk_count() const { return audio_chunk_count; }
};

#endif // SPX_SIMPLE_AUDIO_WRITER_H
