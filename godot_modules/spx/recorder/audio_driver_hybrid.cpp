/**************************************************************************/
/*  audio_driver_hybrid.cpp                                               */
/**************************************************************************/

#include "audio_driver_hybrid.h"

#include "core/os/os.h"
#include "independent_audio_recorder.h"
#include "servers/audio/audio_effect.h"

class SpxAudioCaptureEffect;

// Godot 音频线程中的捕获效果实例：原样透传 Master 混音，同时转换为 int32 PCM
// 交给 HybridAudioDriver。实例由 AudioServer/Ref 管理，不拥有 driver。
// 直接调用方：Godot AudioServer 混音器；顶层调用方：每次音频混音周期。
class SpxAudioCaptureEffectInstance : public AudioEffectInstance {
	GDCLASS(SpxAudioCaptureEffectInstance, AudioEffectInstance);

	// 对效果资源的强引用，用于在音频回调期间安全取得受锁保护的 owner。
	Ref<SpxAudioCaptureEffect> base;
	// 音频线程复用的立体声 float -> int32 转换缓冲，避免把临时指针跨回调保存。
	Vector<int32_t> conversion_buffer;

protected:
	// Godot GDCLASS 要求保留绑定入口；本内部类没有暴露给脚本的方法。
	static void _bind_methods() {}

public:
	void set_base(const Ref<SpxAudioCaptureEffect> &p_base) { base = p_base; }
	void process(const AudioFrame *p_src_frames, AudioFrame *p_dst_frames, int p_frame_count) override;
	bool process_silence() const override { return false; }
};

// 安装到 Godot Master Bus 的捕获资源。
// owner 是借用指针，detach() 与音频线程 instantiate/process 通过 owner_mutex 同步，
// 保证 HybridAudioDriver 销毁时不会留下可回调的悬空地址。
class SpxAudioCaptureEffect : public AudioEffect {
	GDCLASS(SpxAudioCaptureEffect, AudioEffect);
	friend class SpxAudioCaptureEffectInstance;

	mutable Mutex owner_mutex; // 保护音频线程读取与主线程 detach 对 owner 的并发访问。
	HybridAudioDriver *owner = nullptr; // 借用指针；生命周期由 ObsStyleMovieWriter 管理。

protected:
	// 内部 AudioEffect 不向脚本暴露属性，但 GDCLASS 仍需绑定入口。
	static void _bind_methods() {}

public:
	explicit SpxAudioCaptureEffect(HybridAudioDriver *p_owner) :
			owner(p_owner) {}

	void detach() {
		MutexLock lock(owner_mutex);
		owner = nullptr;
	}

	// 直接调用方：Godot AudioServer；顶层调用方：音频总线效果实例化流程。
	Ref<AudioEffectInstance> instantiate() override {
		Ref<SpxAudioCaptureEffectInstance> instance;
		instance.instantiate();
		instance->set_base(Ref<SpxAudioCaptureEffect>(this));
		return instance;
	}
};

// Godot 音频线程回调：先无损透传，再捕获立体声样本。
// 直接调用方：AudioServer；顶层调用方：Godot 音频混音线程。
// Godot 规则：这里不能触碰 SceneTree/FileAccess，也不能等待录制线程；共享状态必须加锁或原子访问。
void SpxAudioCaptureEffectInstance::process(const AudioFrame *p_src_frames, AudioFrame *p_dst_frames, int p_frame_count) {
	for (int i = 0; i < p_frame_count; i++) {
		p_dst_frames[i] = p_src_frames[i];
	}

	if (base.is_null()) {
		return;
	}

	MutexLock lock(base->owner_mutex);
	if (base->owner == nullptr) {
		return;
	}

	conversion_buffer.resize(p_frame_count * 2);
	for (int i = 0; i < p_frame_count; i++) {
		conversion_buffer.write[i * 2] = int32_t(CLAMP(p_src_frames[i].left, -1.0f, 1.0f) * 2147483647.0f);
		conversion_buffer.write[i * 2 + 1] = int32_t(CLAMP(p_src_frames[i].right, -1.0f, 1.0f) * 2147483647.0f);
	}
	base->owner->capture_audio_data(conversion_buffer.ptr(), p_frame_count, 1);
}

HybridAudioDriver::~HybridAudioDriver() {
	finish();
}

// 建立环形缓冲并把捕获效果安装到 Master Bus（Godot 规定索引 0 为 Master）。
// 直接调用方：ObsStyleMovieWriter::setup_audio_capture()；顶层调用方：Go/命令行开始录制。
// AudioServer 的 Bus 效果增删必须在主线程完成。
Error HybridAudioDriver::init(int p_mix_rate, AudioDriver::SpeakerMode p_speaker_mode) {
	ERR_FAIL_COND_V(initialized.is_set(), ERR_ALREADY_IN_USE);
	ERR_FAIL_NULL_V(AudioServer::get_singleton(), ERR_UNCONFIGURED);

	mix_rate = p_mix_rate;
	speaker_mode = p_speaker_mode;
	channels = 2;

	const int requested_frames = MAX(1, int(mix_rate * buffer_length_seconds));
	buffer_power = 0;
	while ((1 << buffer_power) < requested_frames) {
		buffer_power++;
	}
	capture_buffer = RingBuffer<AudioFrame>(buffer_power);

	Ref<SpxAudioCaptureEffect> effect;
	effect.instantiate(this);
	capture_effect_owner = effect.ptr();
	capture_effect = effect;
	capture_bus = 0; // Godot 约定 Master 总线始终是索引 0。
	AudioServer::get_singleton()->add_bus_effect(capture_bus, capture_effect);
	initialized.set();

	if (OS::get_singleton()->is_stdout_verbose()) {
		print_line(vformat("SPX recorder audio capture initialized: %d Hz, %d channels", mix_rate, channels));
	}
	return OK;
}

void HybridAudioDriver::start() {
	// The AudioEffect becomes active as soon as it is inserted into Master.
	// Godot AudioEffect 插入 Master Bus 后会立即参与混音，不需要额外启动动作。
}

// 先让 effect 与 owner 脱钩，再从 AudioServer 移除并释放 Ref。
// 直接调用方：finish()；顶层调用方：录制结束/ObsStyleMovieWriter 销毁。
// detach 必须早于 unref：音频线程可能暂时仍持有 effect instance 的 Ref。
void HybridAudioDriver::_remove_capture_effect() {
	if (capture_effect.is_null()) {
		return;
	}

	if (capture_effect_owner != nullptr) {
		capture_effect_owner->detach();
	}
	AudioServer *audio_server = AudioServer::get_singleton();
	if (audio_server != nullptr && capture_bus >= 0 && capture_bus < audio_server->get_bus_count()) {
		for (int i = audio_server->get_bus_effect_count(capture_bus) - 1; i >= 0; i--) {
			if (audio_server->get_bus_effect(capture_bus, i) == capture_effect) {
				audio_server->remove_bus_effect(capture_bus, i);
				break;
			}
		}
	}
	capture_effect.unref();
	capture_effect_owner = nullptr;
	capture_bus = -1;
}

// 幂等关闭捕获、清空借用 recorder 列表和音频缓冲。
// 直接调用方：ObsStyleMovieWriter::restore_audio_driver()/析构；顶层调用方：停止录制或模块退出。
void HybridAudioDriver::finish() {
	if (!initialized.is_set()) {
		return;
	}

	recording_enabled.clear();
	_remove_capture_effect();
	{
		MutexLock lock(recorders_mutex);
		registered_recorders.clear();
	}
	_clear_capture_buffer();
	initialized.clear();
}

void HybridAudioDriver::_clear_capture_buffer() {
	MutexLock lock(data_mutex);
	capture_buffer = RingBuffer<AudioFrame>(buffer_power);
}

void HybridAudioDriver::enable_recording(bool p_enable) {
	if (recording_enabled.is_set() == p_enable) {
		return;
	}
	if (p_enable) {
		_clear_capture_buffer();
		recording_enabled.set();
	} else {
		recording_enabled.clear();
	}
}

// 把音频回调的交错 PCM 写入兼容缓冲，并同步扇出到已注册独立录音器。
// 直接调用方：SpxAudioCaptureEffectInstance::process()；顶层调用方：Godot 音频混音线程。
// registered_recorders 是借用指针；删除 recorder 前必须先 unregister，并等待本回调退出互斥区。
void HybridAudioDriver::capture_audio_data(const int32_t *p_buffer, int p_frames, int p_channel_pairs) {
	if (!recording_enabled.is_set() || !initialized.is_set() || p_buffer == nullptr || p_frames <= 0) {
		return;
	}

	const int actual_channels = MAX(1, p_channel_pairs * 2);
	{
		MutexLock lock(data_mutex);
		for (int i = 0; i < p_frames; i++) {
			AudioFrame frame;
			frame.left = float(p_buffer[i * actual_channels]) / 2147483648.0f;
			frame.right = actual_channels > 1 ? float(p_buffer[i * actual_channels + 1]) / 2147483648.0f : frame.left;
			if (capture_buffer.space_left() == 0) {
				AudioFrame discarded;
				capture_buffer.read(&discarded, 1);
			}
			capture_buffer.write(&frame, 1);
		}
	}

	MutexLock recorders_lock(recorders_mutex);
	for (IndependentAudioRecorder *recorder : registered_recorders) {
		if (recorder != nullptr && recorder->is_recording()) {
			recorder->on_audio_output(p_buffer, p_frames);
		}
	}
}

// 读取 MovieWriter 兼容缓冲；不足部分补静音并始终返回请求帧数。
// 直接调用方：需要拉取实时 PCM 的 recorder/MovieWriter；顶层调用方：录制音频消费流程。
int HybridAudioDriver::get_captured_audio_data(int32_t *p_output_buffer, int p_requested_frames) {
	ERR_FAIL_NULL_V(p_output_buffer, 0);
	ERR_FAIL_COND_V(p_requested_frames < 0, 0);

	MutexLock lock(data_mutex);
	const int frames_to_read = MIN(p_requested_frames, capture_buffer.data_left());
	Vector<AudioFrame> frames;
	frames.resize(frames_to_read);
	if (frames_to_read > 0) {
		capture_buffer.read(frames.ptrw(), frames_to_read);
	}
	for (int i = 0; i < frames_to_read; i++) {
		p_output_buffer[i * 2] = int32_t(CLAMP(frames[i].left, -1.0f, 1.0f) * 2147483647.0f);
		p_output_buffer[i * 2 + 1] = int32_t(CLAMP(frames[i].right, -1.0f, 1.0f) * 2147483647.0f);
	}
	for (int i = frames_to_read * 2; i < p_requested_frames * 2; i++) {
		p_output_buffer[i] = 0;
	}
	return p_requested_frames;
}

int HybridAudioDriver::get_available_frames() const {
	MutexLock lock(data_mutex);
	return capture_buffer.data_left();
}

bool HybridAudioDriver::has_audio_data() const {
	return get_available_frames() > 0;
}

// 注册借用 recorder，供音频线程扇出样本；重复注册会被忽略。
// 直接调用方：ObsStyleMovieWriter::setup_components()；顶层调用方：开始录制。
void HybridAudioDriver::register_audio_recorder(IndependentAudioRecorder *p_recorder) {
	ERR_FAIL_NULL(p_recorder);
	MutexLock lock(recorders_mutex);
	for (IndependentAudioRecorder *recorder : registered_recorders) {
		if (recorder == p_recorder) {
			return;
		}
	}
	registered_recorders.push_back(p_recorder);
}

// 在 recorder 停止/销毁前撤销借用指针；直接调用方：ObsStyleMovieWriter 清理流程。
void HybridAudioDriver::unregister_audio_recorder(IndependentAudioRecorder *p_recorder) {
	if (p_recorder == nullptr) {
		return;
	}
	MutexLock lock(recorders_mutex);
	for (int i = 0; i < registered_recorders.size(); i++) {
		if (registered_recorders[i] == p_recorder) {
			registered_recorders.remove_at(i);
			return;
		}
	}
}

int HybridAudioDriver::get_registered_recorder_count() const {
	MutexLock lock(recorders_mutex);
	return registered_recorders.size();
}
