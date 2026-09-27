/**************************************************************************/
/*  godot.audio.position.worklet.js                                                      */
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

/*
 * Godot 音频播放位置 Worklet 的 SPX 覆盖版本，由 SCsub 替换上游同名文件。
 * 直接调用方：library_godot_audio.js 创建的 position-reporting AudioWorkletNode；
 * 顶层来源：Godot 音频播放位置/结束状态查询。更新 Godot 时需核对消息协议。
 * process() 运行在实时音频线程，不能访问 DOM 或执行阻塞工作；位置通过 MessagePort
 * 限频发送到主线程，返回 false 表示按 AudioWorklet 规则永久停止该处理器。
 */

const POST_THRESHOLD_S = 0.1;

class GodotPositionReportingProcessor extends AudioWorkletProcessor {
	constructor(...args) {
		super(...args);
		this.lastPostTime = currentTime;
		this.position = 0;
		this.ended = false;

		this.port.onmessage = (event) => {
			const message = event?.['data'];
			if (message?.['type'] === 'ended') {
				this.ended = true;
				return;
			}
			if (message?.['type'] === 'clear') {
				this.ended = false;
				this.position = Number(message['data']) || 0;
				this.lastPostTime = currentTime;
			}
		};
	}

	process(inputs, _outputs, _parameters) {
		if (this.ended) {
			return false;
		}

		if (inputs.length > 0) {
			const input = inputs[0];
			if (input.length > 0) {
				this.position += input[0].length;
			}
		}

		// Posting messages is expensive. Let's limit the number of posts.
		if (currentTime - this.lastPostTime > POST_THRESHOLD_S) {
			this.lastPostTime = currentTime;
			this.port.postMessage({ 'type': 'position', 'data': this.position });
		}

		return true;
	}
}

registerProcessor('godot-position-reporting-processor', GodotPositionReportingProcessor);
