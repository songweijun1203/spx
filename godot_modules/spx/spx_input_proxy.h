/**************************************************************************/
/*  spx_input_proxy.h                                                    */
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

#ifndef SPX_INPUT_PROXY_H
#define SPX_INPUT_PROXY_H

#include "gdextension_spx_ext.h"
#include "scene/main/node.h"

// 挂在 SPX 场景树中的输入事件代理，将 Godot InputEvent 转成稳定 ABI 回调交给 Go。
// 直接调用方：Godot Viewport 的输入传播（input override）；创建方为 SpxInputMgr。
// 顶层来源：操作系统/浏览器键鼠事件；最终消费者是 Go runtime 的事件循环。
// Godot 规则：Node 只有进入 SceneTree 并启用 process_input 后才接收 input；事件按
// Viewport 输入传播顺序到达，回调运行在主线程，不能在此阻塞或修改 Go 调度状态。
class SpxInputProxy : public Node {
	GDCLASS(SpxInputProxy, Node);

public:
	// 直接调用方：SpxInputMgr::on_start；用于在 add_child 后显式启用输入处理。
	void ready();

protected:
	// Godot 引擎回调；顶层来源为平台输入队列，经 SPX_CALLBACK 转发到 Go。
	void input(const Ref<InputEvent> &p_event) override;
};

#endif //SPX_INPUT_PROXY_H
