/**************************************************************************/
/*  spx_realtime_recorder.h                                               */
/**************************************************************************/

#ifndef SPX_REALTIME_RECORDER_H
#define SPX_REALTIME_RECORDER_H

#include "core/error/error_list.h"
#include "core/math/vector2i.h"
#include "core/string/ustring.h"

// SPX 实时录制器抽象，由桌面 ObsStyleMovieWriter 与 Web MovieWriterWebM 实现。
// 直接调用方：MovieRecorderManager；顶层调用方：Go 录制 API 或命令行电影回调。
// 它刻意绕过 MovieWriter::add_frame，因为该接口的音频路径绑定 Godot 离线
// AudioDriverDummy，无法捕获实时游戏 Master 总线。
class SpxRealtimeRecorder {
public:
	virtual ~SpxRealtimeRecorder() = default;

	virtual bool handles_realtime_file(const String &p_path) const = 0;
	virtual Error begin_realtime(const Size2i &p_movie_size, uint32_t p_fps, const String &p_base_path) = 0;
	virtual void add_realtime_frame() = 0;
	virtual void end_realtime() = 0;
};

#endif // SPX_REALTIME_RECORDER_H
