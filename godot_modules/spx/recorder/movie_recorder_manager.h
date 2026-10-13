/**************************************************************************/
/*  movie_recorder_manager.h                                             */
/**************************************************************************/

#ifndef SPX_MOVIE_RECORDER_MANAGER_H
#define SPX_MOVIE_RECORDER_MANAGER_H

#include "core/math/vector2i.h"
#include "core/string/ustring.h"

class SpxRealtimeRecorder;

// SPX 录制流程的全局协调器，将 Go 主动录制和 Godot 命令行 --write-movie 统一到
// SpxRealtimeRecorder 生命周期，并在主循环 AFTER_DRAW 阶段采集最终画面。
// 直接调用方：Web 录制导出函数、register_recorder_types 注册的电影回调；
// 顶层调用方：页面/Go Web 录制控制或 Godot 命令行录制入口。
// Godot 规则：渲染画面应在 draw 完成后读取；回调注销必须早于 recorder 实例销毁。
class MovieRecorderManager {
public:
	// Go 主动录制使用的参数；宽高为 0 时使用当前根 Viewport 尺寸。
	struct RecordingConfig {
		String output_path; // 最终输出路径，也是 writer 选择格式的依据。
		uint32_t video_fps = 30; // 输出帧率。
		uint32_t video_width = 0; // 输出宽度，0 表示自动。
		uint32_t video_height = 0; // 输出高度，0 表示自动。
		float video_quality = 0.85f; // 有损帧编码质量。
		uint32_t audio_sample_rate = 48000; // 音频采样率。
		uint32_t audio_channels = 2; // 音频通道数。
		bool enable_audio = true; // 是否捕获 Master 总线。
		bool realtime_mode = true; // 是否使用实时独立线程录制器。

		RecordingConfig() = default;
		explicit RecordingConfig(const String &p_path) :
				output_path(p_path) {}
	};

	static void initialize();
	static void shutdown();

	static Error start_recording(const RecordingConfig &p_config);
	static Error stop_recording();
	static Error pause_recording();
	static Error resume_recording();

	static bool is_recording();
	static bool is_initialized();
	static bool has_active_instance();
	static String get_current_output_path();
	static float get_recording_duration();

private:
	enum InstanceState {
		NONE,
		STARTED,
	};

	static SpxRealtimeRecorder *instance; // 当前 writer 的借用指针，由注册中心持有实际对象。
	static InstanceState state; // Manager 自身的一次性运行状态。
	static RecordingConfig current_config; // 当前主动录制配置快照。
	static Size2i default_movie_size; // 未显式配置时采用的输出尺寸。
	static uint32_t default_fps; // 未显式配置时采用的帧率。
	static uint64_t recording_start_time; // OS 单调时钟起点（微秒）。
	static uint64_t callback_registration; // MainLoopPhaseCallbackBus 注册句柄。
	static bool command_line_recording; // 当前会话是否由 Godot 命令行发起。

	static Error _begin(const Size2i &p_movie_size, uint32_t p_fps, const String &p_path, bool p_command_line);
	static void _finish();

	static bool _movie_claim(void *p_userdata, const String &p_movie_path);
	static bool _movie_requires_live_audio(void *p_userdata, const String &p_movie_path);
	static Error _movie_begin(void *p_userdata, const Size2i &p_movie_size, uint32_t p_fps, const String &p_movie_path);
	static void _movie_frame(void *p_userdata);
	static void _movie_end(void *p_userdata);
	static void _destroy(void *p_userdata);
};

#endif // SPX_MOVIE_RECORDER_MANAGER_H
