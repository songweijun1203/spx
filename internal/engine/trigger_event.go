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

package engine

import "sync"

type TriggerEvent struct {
	// 触发事件的源精灵和被触发的目标精灵。
	Src *Sprite
	Dst *Sprite
}

type triggerEventQueue struct {
	mu sync.Mutex
	// pending 接收当前帧或跨回调到达的新事件，不在写入时直接消费。
	pending []TriggerEvent
	// ready 保存上一帧边界转入、等待 Game.OnEngineRender 消费的事件。
	ready []TriggerEvent
}

var triggerEvents triggerEventQueue

func GetTriggerEvents(dst []TriggerEvent) []TriggerEvent {
	return triggerEvents.drain(dst)
}

// resetTriggerEvents 清空生命周期切换时尚未处理的触发事件。
func resetTriggerEvents() {
	triggerEvents.reset()
}

// cacheTriggerEvents 把本帧开始前收到的 pending 事件转入 ready。
// 调用方：internal/engine.onUpdate()；它只做帧边界交换，不执行碰撞回调。
func cacheTriggerEvents() {
	triggerEvents.cache()
}

// enqueueTriggerEvent 将一次已由 Godot/物理回调确认的触发事件写入 pending。
// 直接调用方：internal/engine.Sprite.OnTriggerEnter()；
// 它由 gdengine.onTriggerEnter() 在 Native/Web FFI 回调链中转发而来。
func enqueueTriggerEvent(src, dst *Sprite) {
	triggerEvents.mu.Lock()
	triggerEvents.pending = append(triggerEvents.pending, TriggerEvent{Src: src, Dst: dst})
	triggerEvents.mu.Unlock()
}

// drain 取出当前 ready 队列并清空它，供 Game.OnEngineRender() 后续处理。
func (q *triggerEventQueue) drain(dst []TriggerEvent) []TriggerEvent {
	q.mu.Lock()
	dst = append(dst, q.ready...)
	clear(q.ready)
	q.ready = q.ready[:0]
	q.mu.Unlock()
	return dst
}

func (q *triggerEventQueue) reset() {
	q.mu.Lock()
	clear(q.pending)
	clear(q.ready)
	q.pending = nil
	q.ready = nil
	q.mu.Unlock()
}

// cache 在锁内完成 pending -> ready 的交换，避免消费过程中丢失新到达事件。
func (q *triggerEventQueue) cache() {
	q.mu.Lock()
	q.ready = append(q.ready, q.pending...)
	clear(q.pending)
	q.pending = q.pending[:0]
	q.mu.Unlock()
}
