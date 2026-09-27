/**************************************************************************/
/*  post_merge_processor.h                                               */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/

#ifndef SPX_POST_MERGE_PROCESSOR_H
#define SPX_POST_MERGE_PROCESSOR_H

#include "core/error/error_macros.h"
#include "core/io/file_access.h"
#include "core/string/ustring.h"

// 独立音视频录制后的合并器，可调用系统 FFmpeg，也可直接解析并重写 AVI 容器。
// 直接调用方：ObsStyleMovieWriter::perform_post_merge；
// 顶层调用方：Go 录制停止或 Godot 命令行录制结束。
// Godot 规则：FileAccess 用 Ref 管理句柄，OS::execute 受平台能力限制；Web 平台通常不支持
// 系统 FFmpeg，调用前必须通过 is_method_available/get_recommended_method 选择方案。
class PostMergeProcessor {
public:
	// 可选的音视频合并后端。
	enum MergeMethod {
		METHOD_FFMPEG_SYSTEM, // 调用系统 FFmpeg。
		METHOD_CUSTOM_AVI, // 模块内直接重写 AVI 容器。
		METHOD_NONE // 不合并，保留独立文件。
	};

	// 一次后处理会话的配置。
	struct MergeConfig {
		MergeMethod method = METHOD_FFMPEG_SYSTEM; // 选定的合并后端。
		bool keep_intermediate_files = false; // 成功后是否保留原音视频文件。
		bool enable_debug_output = false; // 是否输出详细诊断。
		String ffmpeg_path = "ffmpeg"; // FFmpeg 可执行文件路径。

		// 自定义 AVI 合并时的数据块交错策略。
		enum InterleaveStrategy {
			INTERLEAVE_SIMPLE_ALTERNATE, // 音视频块简单交替，兼容旧实现。
			INTERLEAVE_TIMESTAMP_BASED, // 按计算出的时间戳排序。
			INTERLEAVE_BUFFERED_TIMESTAMP // 按时间戳排序并做 I/O 缓冲。
		};
		InterleaveStrategy interleave_strategy = INTERLEAVE_TIMESTAMP_BASED; // 当前交错算法。
		uint32_t buffer_duration_ms = 500; // 缓冲交错窗口（毫秒）。
		bool validate_sync = true; // 是否检查音视频时间漂移。
		uint64_t max_av_drift_us = 40000; // 允许的最大音视频漂移（40 ms）。

		MergeConfig() {}
	};

	// 合并结果及清理状态，作为值返回给 MovieWriter。
	struct MergeResult {
		Error error_code = OK; // Godot 错误码。
		String output_file_path; // 成功时的最终文件路径。
		String error_message; // 面向日志的错误说明。
		float merge_duration_seconds = 0.0f; // 合并耗时。
		bool intermediate_files_cleaned = false; // 中间文件是否已按配置清理。

		MergeResult() {}
	};

private:
	MergeConfig config; // 当前合并配置。

	// 系统 FFmpeg 后端。
	Error ffmpeg_system_merge(const String &video_path, const String &audio_path, const String &output_path, MergeResult &result);

	// 模块内 AVI 容器合并后端。
	Error custom_avi_merge(const String &video_path, const String &audio_path, const String &output_path, MergeResult &result);

	// 从 AVI idx1 推导出的带时间戳数据块，用于统一排序交错。
	struct TimestampedChunk {
		enum ChunkType {
			VIDEO_CHUNK,
			AUDIO_CHUNK
		};

		ChunkType type; // 视频或音频块。
		uint64_t timestamp_us; // 相对录制起点的时间戳（微秒）。
		uint64_t file_offset; // 在源文件中的绝对偏移。
		uint32_t chunk_size; // 数据负载长度。
		uint32_t index; // 源索引序号，用于诊断稳定排序。

		// Vector 排序所需的时间戳比较。
		bool operator<(const TimestampedChunk &other) const {
			return timestamp_us < other.timestamp_us;
		}
	};

	// 解析后的 AVI 元数据；字段保持 RIFF 小端语义。
	struct AviFileInfo {
		uint32_t microsec_per_frame; // avih 单帧时长。
		uint32_t max_bytes_per_sec; // avih 建议最大吞吐。
		uint32_t total_frames; // 视频总帧数。
		uint32_t streams; // 流数量。
		uint32_t width; // 视频宽度。
		uint32_t height; // 视频高度。

		// AVI 流描述。
		struct StreamInfo {
			char fourcc_type[4]; // 流类型：vids 或 auds。
			char fourcc_handler[4]; // 编码：MJPG 或 PCM。
			uint32_t scale; // 时间基准分母单位。
			uint32_t rate; // 时间基准速率，fps = rate / scale。
			uint32_t length; // 帧或采样块数量。
			uint32_t sample_size; // 固定采样单元字节数。
		};

		Vector<StreamInfo> streams_info; // strh 中解析出的各流信息。

		// movi 文件偏移。
		uint64_t movi_list_offset; // LIST movi 块头偏移。
		uint64_t movi_data_offset; // 首个媒体数据块偏移。
		uint64_t movi_data_size; // movi 负载总长度。

		// idx1 索引信息。
		struct IndexEntry {
			char fourcc[4]; // 数据块类型标记。
			uint32_t flags; // AVI 索引标志，如关键帧。
			uint32_t chunk_offset; // 相对 movi 数据起点的偏移。
			uint32_t chunk_size; // 数据块长度。
		};
		Vector<IndexEntry> index_entries; // idx1 条目；缺失时可扫描 movi 重建。

		AviFileInfo() {
			microsec_per_frame = 0;
			max_bytes_per_sec = 0;
			total_frames = 0;
			streams = 0;
			width = 0;
			height = 0;
			movi_list_offset = 0;
			movi_data_offset = 0;
			movi_data_size = 0;
		}
	};

	// AVI 解析、头部写入与媒体块交错。
	Error parse_avi_file(const String &file_path, AviFileInfo &avi_info);
	Error parse_hdrl_chunk(Ref<FileAccess> file, AviFileInfo &avi_info, uint32_t chunk_size);
	Error parse_stream_header(Ref<FileAccess> file, AviFileInfo::StreamInfo &stream_info, uint32_t chunk_size);
	Error parse_idx1_chunk(Ref<FileAccess> file, AviFileInfo &avi_info, uint32_t chunk_size);
	Error write_merged_avi_header(Ref<FileAccess> output_file, const AviFileInfo &video_info, const AviFileInfo &audio_info);
	Error write_video_stream_header(Ref<FileAccess> output_file, const AviFileInfo &video_info);
	Error write_audio_stream_header(Ref<FileAccess> output_file, const AviFileInfo &audio_info);
	Error interleave_avi_data(const String &video_path, const String &audio_path, Ref<FileAccess> output_file, const AviFileInfo &video_info, const AviFileInfo &audio_info);
	Error interleave_avi_data_timestamped(const String &video_path, const String &audio_path, Ref<FileAccess> output_file, const AviFileInfo &video_info, const AviFileInfo &audio_info);
	Error write_merged_avi_index(Ref<FileAccess> output_file, const Vector<AviFileInfo::IndexEntry> &merged_index);

	// 基于时间戳交错的辅助计算。
	void calculate_chunk_timestamps(const Vector<AviFileInfo::IndexEntry> &chunks, const AviFileInfo &info, bool is_video, Vector<TimestampedChunk> &timestamped_chunks);
	uint32_t calculate_optimal_audio_chunk_size(uint32_t video_fps, uint32_t sample_rate);
	bool validate_av_sync(uint64_t video_ts, uint64_t audio_ts);
	Error write_timestamped_chunk(Ref<FileAccess> input_file, Ref<FileAccess> output_file, const TimestampedChunk &chunk, bool is_video, uint32_t &chunk_offset, Vector<AviFileInfo::IndexEntry> &merged_index);

	// 平台能力、文件检查与中间文件清理。
	bool check_ffmpeg_availability();
	bool file_exists(const String &path);
	Error cleanup_intermediate_files(const String &video_path, const String &audio_path);
	uint64_t get_file_size(const String &path);
	void scan_movi_chunks(Ref<FileAccess> file, uint64_t movi_offset, uint32_t movi_size, Vector<AviFileInfo::IndexEntry> &chunks);

public:
	PostMergeProcessor();
	~PostMergeProcessor();

	// 设置后续 merge_files 使用的配置快照。
	void set_config(const MergeConfig &p_config);
	const MergeConfig &get_config() const { return config; }

	// 合并主入口。直接调用方：ObsStyleMovieWriter::perform_post_merge；返回错误与清理详情。
	MergeResult merge_files(const String &video_path, const String &audio_path, const String &output_path);

	// 查询指定后端在当前 OS/构建平台是否可用。
	bool is_method_available(MergeMethod method);

	// 按当前平台能力选择推荐后端。
	MergeMethod get_recommended_method();

	// 返回用于日志展示的后端名称。
	String get_method_name(MergeMethod method);

	// 在创建输出文件前校验路径、输入文件和配置。
	Error validate_merge_request(const String &video_path, const String &audio_path, const String &output_path);
};

#endif // SPX_POST_MERGE_PROCESSOR_H
