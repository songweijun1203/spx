/**************************************************************************/
/*  audio_driver_hybrid.h                                                 */
/**************************************************************************/

#ifndef SPX_AUDIO_DRIVER_HYBRID_H
#define SPX_AUDIO_DRIVER_HYBRID_H

#include "core/math/audio_frame.h"
#include "core/os/mutex.h"
#include "core/templates/ring_buffer.h"
#include "core/templates/safe_refcount.h"
#include "core/templates/vector.h"
#include "servers/audio/audio_effect.h"
#include "servers/audio_server.h"

class IndependentAudioRecorder;
class SpxAudioCaptureEffect;

// 通过普通 AudioEffect 捕获 Master 总线最终混音，并分发给独立录音线程。
// 直接调用方：ObsStyleMovieWriter 和 SpxAudioCaptureEffectInstance；
// 顶层调用方：Go 录制 API 或 Godot 命令行电影录制生命周期。
// Godot 规则：AudioEffect::process 运行在音频线程，本类因此用 SafeFlag/Mutex 隔离共享状态；
// 它不替换 AudioDriver::singleton，也不要求修改 AudioServer 的引擎钩子。
class HybridAudioDriver {
	SafeFlag recording_enabled; // 音频线程可读取的录制开关。
	SafeFlag initialized; // 捕获效果和缓冲区是否已完成初始化。
	int mix_rate = 44100; // Master 总线采样率。
	AudioDriver::SpeakerMode speaker_mode = AudioDriver::SPEAKER_MODE_STEREO; // Godot 扬声器布局。
	int channels = 2; // 由 speaker_mode 推导的交错通道数。

	mutable Mutex data_mutex; // 保护 capture_buffer 的音频线程/录制线程并发访问。
	RingBuffer<AudioFrame> capture_buffer; // Master 混音的短期环形缓冲。
	int buffer_power = 0; // Godot RingBuffer 以 2 的幂表示容量。
	float buffer_length_seconds = 0.2f; // 捕获缓冲目标时长。

	Vector<IndependentAudioRecorder *> registered_recorders; // 借用指针；所有权属于 MovieWriter。
	mutable Mutex recorders_mutex; // 保护录音器注册表与音频回调遍历。

	Ref<AudioEffect> capture_effect; // AudioServer 总线持有期间同时由 Ref 保活。
	SpxAudioCaptureEffect *capture_effect_owner = nullptr; // capture_effect 的已校验具体类型借用指针。
	int capture_bus = -1; // 安装效果的 Master 总线索引，-1 表示未安装。

	void _clear_capture_buffer();
	void _remove_capture_effect();

public:
	HybridAudioDriver() = default;
	~HybridAudioDriver();

	// 直接调用方：ObsStyleMovieWriter::setup_audio_capture；必须在 Godot 主线程安装/移除总线效果。
	Error init(int p_mix_rate, AudioDriver::SpeakerMode p_speaker_mode);
	void start();
	void finish();

	void enable_recording(bool p_enable);
	bool is_recording_enabled() const { return recording_enabled.is_set(); }

	int get_channels() const { return channels; }
	int get_mix_rate() const { return mix_rate; }

	// 直接调用方：AudioEffectInstance 音频线程回调；顶层调用方：AudioServer 混音周期。
	void capture_audio_data(const int32_t *p_buffer, int p_frames, int p_channel_pairs);
	int get_captured_audio_data(int32_t *p_output_buffer, int p_requested_frames);

	int get_available_frames() const;
	bool has_audio_data() const;

	void register_audio_recorder(IndependentAudioRecorder *p_recorder);
	void unregister_audio_recorder(IndependentAudioRecorder *p_recorder);
	int get_registered_recorder_count() const;
};

#endif // SPX_AUDIO_DRIVER_HYBRID_H
