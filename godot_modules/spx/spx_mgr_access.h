/**************************************************************************/
/*  spx_mgr_access.h                                                      */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/
/* Copyright (c) 2014-present Godot Engine contributors (see AUTHORS.md). */
/* Copyright (c) 2007-2014 Juan Linietsky, Ariel Manzur.                  */
/*                                                                        */
/* Permission is hereby granted, free of charge, to any person obtaining  */
/* a copy of this software and associated documentation files (the        */
/* "Software"), to deal in the Software without restriction, including    */
/* without limitation the rights to use, copy, modify, merge, publish,    */
/* distribute, sublicense, and/or sell copies of the Software, and to     */
/* permit persons to whom the Software is furnished to do so, subject to  */
/* the following conditions:                                              */
/*                                                                        */
/* The above copyright notice and this permission notice shall be         */
/* included in all copies or substantial portions of the Software.        */
/*                                                                        */
/* THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,        */
/* EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF     */
/* MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. */
/* IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY   */
/* CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,   */
/* TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE      */
/* SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.                 */
/**************************************************************************/

#ifndef SPX_MGR_ACCESS_H
#define SPX_MGR_ACCESS_H

class SpxEngine;
class SpxAudioBusPool;

// 当前 SpxEngine 所有 Manager 的快捷访问宏。
// 返回值均是 singleton 内的非拥有型指针，只能在引擎已初始化且未 shutdown 时使用；
// 若后续操作触碰 Node/SceneTree，还必须遵守 Godot 主线程规则。
// 直接调用方：各 Manager/对象实现；顶层调用方：Go SPX API 或 Godot 主循环回调。
#define inputMgr SpxEngine::get_singleton()->get_input()
#define audioMgr SpxEngine::get_singleton()->get_audio()
#define physicsMgr SpxEngine::get_singleton()->get_physics()
#define spriteMgr SpxEngine::get_singleton()->get_sprite()
#define uiMgr SpxEngine::get_singleton()->get_ui()
#define sceneMgr SpxEngine::get_singleton()->get_scene()
#define cameraMgr SpxEngine::get_singleton()->get_camera()
#define platformMgr SpxEngine::get_singleton()->get_platform()
#define resMgr SpxEngine::get_singleton()->get_res()
#define debugMgr SpxEngine::get_singleton()->get_debug()
#define navigationMgr SpxEngine::get_singleton()->get_navigation()
#define penMgr SpxEngine::get_singleton()->get_pen()
#define tilemapMgr SpxEngine::get_singleton()->get_tilemap()
#define tilemapparserMgr SpxEngine::get_singleton()->get_tilemapparser()

// 音频总线池是独立进程级单例，宏不转移其所有权。
#define audioPool SpxAudioBusPool::get_singleton()
// 当前 C++ -> Go/JS 回调表的借用指针，只允许在同步调用期间使用。
#define SPX_CALLBACK SpxEngine::get_singleton()->get_callbacks()

#endif // SPX_MGR_ACCESS_H
