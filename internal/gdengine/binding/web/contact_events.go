//go:build js && wasm

/*
 * Copyright (c) 2021 The XGo Authors (xgo.dev). All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package webffi

import (
	"encoding/binary"
	"syscall/js"
)

const (
	contactCollisionEnter = 1
	contactCollisionStay  = 2
	contactCollisionExit  = 3
	contactTriggerEnter   = 4
	contactTriggerStay    = 5
	contactTriggerExit    = 6
	contactEventBytes     = 4 + 8 + 8 // kind, self ID, other ID
)

var contactEventsHandle js.Func
var contactEventScratch []byte
var contactEventGeneration uint64

// registerContactEventQueue 注册 Web 专用的批量碰撞/触发事件入口。
// 直接上级：registerWebGlobals()；JavaScript 通过 globalThis.gdspx_on_contact_events 调用。
func registerContactEventQueue() {
	if contactEventsHandle.Type() != js.TypeUndefined {
		return
	}
	contactEventsHandle = js.FuncOf(gdspxContactEvents)
	js.Global().Set("gdspx_on_contact_events", contactEventsHandle)
}

// gdspxContactEvents 把 JavaScript Uint8Array 中的批量事件复制到 Go 并逐条分发。
// 直接上级：library_godot_gdspx.js 刷新接触事件队列时调用已注册的全局函数。
func gdspxContactEvents(this js.Value, args []js.Value) any {
	generation := contactEventGeneration
	if len(args) == 0 || !isByteArray(args[0]) {
		return nil
	}

	events := args[0]
	length := events.Length()
	if length < contactEventBytes {
		return nil
	}

	if cap(contactEventScratch) < length {
		contactEventScratch = make([]byte, length)
	}
	buf := contactEventScratch[:length]
	js.CopyBytesToGo(buf, events)

	// 某个回调可能同步重置/销毁引擎或重新绑定回调表。
	// generation 变化后立即停止，避免把同一批剩余事件错误地交给新会话。
	for i := 0; generation == contactEventGeneration && i+contactEventBytes <= len(buf); i += contactEventBytes {
		kind := int(binary.LittleEndian.Uint32(buf[i : i+4]))
		self := int64(binary.LittleEndian.Uint64(buf[i+4 : i+12]))
		other := int64(binary.LittleEndian.Uint64(buf[i+12 : i+20]))
		dispatchContactEvent(kind, self, other)
	}
	return nil
}

// dispatchContactEvent 根据事件类型调用当前 Web 回调表中的碰撞或触发函数。
// 直接上级：gdspxContactEvents()。
func dispatchContactEvent(kind int, self, other int64) {
	var cb func(int64, int64)
	switch kind {
	case contactCollisionEnter:
		cb = callbacks.OnCollisionEnter
	case contactCollisionStay:
		cb = callbacks.OnCollisionStay
	case contactCollisionExit:
		cb = callbacks.OnCollisionExit
	case contactTriggerEnter:
		cb = callbacks.OnTriggerEnter
	case contactTriggerStay:
		cb = callbacks.OnTriggerStay
	case contactTriggerExit:
		cb = callbacks.OnTriggerExit
	}
	if cb != nil {
		cb(self, other)
	}
}
