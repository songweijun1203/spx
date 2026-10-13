/**************************************************************************/
/*  spx_callback_proxy.h                                                  */
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

#ifndef SPX_CALLBACK_PROXY_H
#define SPX_CALLBACK_PROXY_H

#include "scene/main/node.h"

#include <functional>
#include <utility>

// 把普通 C++ 闭包适配为 Godot 可连接的 Node 方法。
// SpxEngine 将本节点挂入根 Window，再把 SceneTreeTimer.timeout 连接到 `_on_timeout`；
// 这样 Godot signal 只持有合法 Object/Callable，不直接持有裸 C++ lambda。
// 节点加入树后由 SceneTree 拥有，设置、触发和清理 callback 均在 Godot 主线程进行。
class SpxCallbackProxy : public Node {
	GDCLASS(SpxCallbackProxy, Node);

	// 下一次 timeout 要执行的闭包；只保存一个任务，执行前清空以释放对 SpxEngine 的捕获。
	std::function<void()> callback;

	// 直接调用方：Godot signal 系统；顶层调用方：SceneTreeTimer.timeout。
	void on_timeout() {
		// This proxy is only connected to one-shot timers. Drop captures before
		// invoking user code so teardown and re-entrant scheduling cannot retain
		// stale engine state.
		// 代理只连接一次性 timer。先移出并清空成员，再执行用户代码，避免重入调度或
		// 销毁流程继续持有已经失效的引擎状态。
		std::function<void()> pending = std::exchange(callback, {});
		if (pending) {
			pending();
		}
	}

protected:
	// Godot 规定：要被 signal 通过方法名调用的方法必须注册到 ClassDB。
	// 直接调用方：Spx::register_types() 触发的 ClassDB 注册；顶层调用方：模块 SCENE 初始化。
	static void _bind_methods() {
		ClassDB::bind_method(D_METHOD("_on_timeout"), &SpxCallbackProxy::on_timeout);
	}

public:
	// 直接调用方：SpxEngine::_invoke_runtime_reset_delayed()；顶层调用方：Web reset 流程。
	void set_callback(std::function<void()> p_callback) {
		callback = std::move(p_callback);
	}

	// 直接调用方：SpxEngine::_disconnect_reset_timer()/on_destroy()；用于解除对引擎的捕获。
	void clear_callback() {
		callback = {};
	}
};

#endif // SPX_CALLBACK_PROXY_H
