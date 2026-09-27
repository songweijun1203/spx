/**************************************************************************/
/*  spx_manager.h                                                        */
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

#ifndef SPX_MANAGER_H
#define SPX_MANAGER_H

#include "spx_abi.h"
#include "spx_mgr_access.h"

class Node;
class Window;
class SceneTree;

// 所有 SPX 功能 Manager 的生命周期基类。
// SpxEngine 拥有派生 Manager 实例并按固定顺序广播钩子；Manager 自己拥有的 Godot 节点
// 由具体派生类负责创建，加入 SceneTree 后遵守 Godot 的主线程和 queue_free 规则。
// 直接调用方：SpxEngine::_notify_managers()；顶层调用方：Spx 主循环阶段。
class SpxManager {
protected:
	// 从当前 SpxEngine 借用全局对象 ID；只应在主线程创建对象时调用。
	GdInt get_unique_id();
	// 以下是当前引擎树/窗口/根节点的非拥有型访问器，返回指针不得跨生命周期保存。
	SceneTree *get_tree();
	Window *get_root();
	Node *get_spx_root();

public:
	virtual ~SpxManager() = default;
	// 创建完所有 Manager 后调用一次；适合建立依赖 Godot 根节点的资源。
	virtual void on_awake() {}
	// 一局开始或 restart 时调用；直接调用方：SpxEngine::on_awake()/restart()。
	virtual void on_start() {}
	// 每个逻辑帧调用；直接调用方：SpxEngine::on_update()，顶层调用方：Godot 主循环。
	virtual void on_update(float delta) {}
	// 每个物理帧调用；直接调用方：SpxEngine::on_fixed_update()，顶层调用方：Godot 物理循环。
	virtual void on_fixed_update(float delta) {}
	// 释放 Manager 自有状态和节点；直接调用方：SpxEngine::on_destroy()。
	virtual void on_destroy() {}
	// 一局重置时调用；直接调用方：SpxEngine::_do_reset()，顶层调用方：Go reset 请求。
	virtual void on_reset(int reset_code) {}
	// 进程退出通知；直接调用方：SpxEngine::on_exit()，顶层调用方：Go request_exit。
	virtual void on_exit(int exit_code) {}
	// SceneTree 暂停状态变为 true 时调用。
	virtual void on_pause() {}
	// SceneTree 暂停状态变为 false 时调用。
	virtual void on_resume() {}
};

#endif // SPX_MANAGER_H
