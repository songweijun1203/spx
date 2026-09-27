#ifndef SPX_REGISTER_TYPES_H
#define SPX_REGISTER_TYPES_H

#include "modules/register_module_types.h"

// SPX 静态模块的分级初始化入口。
// 直接调用方：Godot 模块注册框架；顶层调用方：Godot main 启动流程。
// Godot 规定：模块必须只在对应 ModuleInitializationLevel 可用后注册依赖该层的能力。
void initialize_spx_module(ModuleInitializationLevel p_level);
// SPX 静态模块的分级反初始化入口，调用顺序与初始化级别相反。
// 直接调用方：Godot 模块反注册框架；顶层调用方：Godot main 退出/初始化失败回滚流程。
void uninitialize_spx_module(ModuleInitializationLevel p_level);

#endif // SPX_REGISTER_TYPES_H
