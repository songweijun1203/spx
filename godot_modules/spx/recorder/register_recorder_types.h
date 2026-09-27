/**************************************************************************/
/*  register_recorder_types.h                                            */
/**************************************************************************/

#ifndef SPX_REGISTER_RECORDER_TYPES_H
#define SPX_REGISTER_RECORDER_TYPES_H

#include "core/string/ustring.h"

class SpxRealtimeRecorder;

// Godot 模块初始化分阶段注册：CORE 创建纯数据设施，SERVERS 注册 MovieWriter。
// 直接调用方：spx/register_types.cpp；顶层调用方：Godot ModuleInitializationLevel。
void initialize_spx_recorder_core();
void uninitialize_spx_recorder_core();
void initialize_spx_recorder_servers();
void uninitialize_spx_recorder_servers();

// 命令行录制格式探测；直接调用方：SPX 的引擎电影回调适配层。
bool spx_recorder_claims_movie(const String &p_path);
SpxRealtimeRecorder *spx_find_realtime_movie_recorder(const String &p_path);

#endif // SPX_REGISTER_RECORDER_TYPES_H
