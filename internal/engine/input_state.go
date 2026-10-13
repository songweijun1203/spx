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

import (
	"slices"
	"sync"
	"sync/atomic"
)

type KeyEvent struct {
	Id        int64
	IsPressed bool
}

type MouseEvent struct {
	Id        int64
	IsPressed bool
}

type keyInputState struct {
	// pending 由平台回调写入，ready 在引擎帧边界发布给唯一消费者。
	mu       sync.Mutex
	pending  []KeyEvent
	ready    []KeyEvent
	pressed  map[int64]bool
	keysDown []int64
}

type mouseInputState struct {
	// buttons 提供无锁即时查询；pending/ready 保留同一帧内的有序边沿。
	mu            sync.Mutex
	buttons       [4]atomic.Bool
	pending       []MouseEvent
	ready         []MouseEvent
	cachedButtons uint8
}

var (
	keyInput   = keyInputState{pressed: make(map[int64]bool)}
	mouseInput mouseInputState
)

// IsMouseButtonPressed reports whether the button is held.
func IsMouseButtonPressed(id int64) bool {
	if id < 0 || id >= int64(len(mouseInput.buttons)) {
		return false
	}
	return mouseInput.buttons[id].Load()
}

// AnyMouseButtonPressed reports whether a primary mouse button is held.
func AnyMouseButtonPressed() bool {
	return IsMouseButtonPressed(1) || IsMouseButtonPressed(2)
}

// GetKeyEvents 取走本帧 ready 中按顺序缓存的键盘按下/抬起边沿，并清空 ready。
// 普通实时输入由 inputEventLoop 调用它；当前脚本事件 API 只把按下边沿转成 OnKey。
func GetKeyEvents(dst []KeyEvent) []KeyEvent {
	return keyInput.drain(dst)
}

// GetKeyInput 供输入录制/回放采样使用：取走键盘边沿，并返回排序后的当前按住键快照。
func GetKeyInput(dst []KeyEvent) ([]KeyEvent, []int64) {
	return keyInput.drainInput(dst)
}

// GetMouseInput 取走本帧鼠标按钮边沿，并返回发布该批边沿时的按住状态位图。
// 实时 inputEventLoop 与输入会话共享此入口，由上层保证每帧只有一个消费者。
func GetMouseInput(dst []MouseEvent) ([]MouseEvent, uint8) {
	return mouseInput.drainInput(dst)
}

// GetMouseEvents 只返回鼠标按钮边沿，作为 GetMouseInput 的便捷封装（会丢弃按住状态快照）。
// 当前输入会话直接使用 GetMouseInput，同时需要边沿和按住状态。
func GetMouseEvents(dst []MouseEvent) []MouseEvent {
	events, _ := GetMouseInput(dst)
	return events
}

// DiscardPendingKeyEvents starts a clean input-session boundary.
func DiscardPendingKeyEvents() {
	keyInput.discard()
}

// DiscardPendingMouseEvents starts a clean input-consumer boundary while
// preserving the current held-button snapshot.
func DiscardPendingMouseEvents() {
	mouseInput.discard()
}

// ResetInputState clears process-wide input state at a game lifecycle boundary.
func ResetInputState() {
	keyInput.reset()
	mouseInput.reset()
}

// onKeyPressed/onKeyReleased 是 Godot 输入回调的 Go 侧入口：记录当前按住状态，
// 并把有序按键边沿放入 pending，等待 onUpdate 帧边界缓存后由输入循环取走。
func onKeyPressed(id int64) {
	queueKeyEvent(id, true)
}

func onKeyReleased(id int64) {
	queueKeyEvent(id, false)
}

func queueKeyEvent(id int64, pressed bool) {
	if !acceptsRuntimeWork() {
		return
	}
	keyInput.mu.Lock()
	if pressed {
		keyInput.pressed[id] = true
	} else {
		delete(keyInput.pressed, id)
	}
	keyInput.pending = append(keyInput.pending, KeyEvent{Id: id, IsPressed: pressed})
	keyInput.mu.Unlock()
}

// onMousePressed/onMouseReleased 是 Godot 鼠标按钮回调的 Go 侧入口：立即更新按住状态，
// 并把去重后的边沿写入 pending，等待下一引擎帧发布。
func onMousePressed(id int64) {
	queueMouseEvent(id, true)
}

func onMouseReleased(id int64) {
	queueMouseEvent(id, false)
}

func queueMouseEvent(id int64, pressed bool) {
	if !acceptsRuntimeWork() || id < 1 || id >= int64(len(mouseInput.buttons)) {
		return
	}
	mouseInput.mu.Lock()
	if mouseInput.buttons[id].Load() == pressed {
		mouseInput.mu.Unlock()
		return
	}
	mouseInput.buttons[id].Store(pressed)
	mouseInput.pending = append(mouseInput.pending, MouseEvent{Id: id, IsPressed: pressed})
	mouseInput.mu.Unlock()
}

// cacheKeyEvents 在引擎帧边界把输入回调写入的 pending 键盘边沿转入 ready；
// ready 随后由 inputEventLoop.GetKeyEvents 或输入会话的 GetKeyInput 消费。
func cacheKeyEvents() {
	keyInput.cache()
}

// cacheMouseEvents 在引擎帧边界把 pending 鼠标边沿转入 ready，并刷新按住状态快照；
// ready/快照随后由实时循环或输入会话中的唯一消费者取走。
func cacheMouseEvents() {
	mouseInput.cache()
}

func (s *keyInputState) drain(dst []KeyEvent) []KeyEvent {
	s.mu.Lock()
	dst = append(dst, s.ready...)
	s.ready = s.ready[:0]
	s.mu.Unlock()
	return dst
}

func (s *keyInputState) drainInput(dst []KeyEvent) ([]KeyEvent, []int64) {
	s.mu.Lock()
	dst = append(dst, s.ready...)
	s.ready = s.ready[:0]
	keysDown := append([]int64(nil), s.keysDown...)
	s.mu.Unlock()
	return dst, keysDown
}

func (s *keyInputState) discard() {
	s.mu.Lock()
	s.pending = s.pending[:0]
	s.ready = s.ready[:0]
	s.rebuildKeysDownLocked()
	s.mu.Unlock()
}

func (s *keyInputState) reset() {
	s.mu.Lock()
	s.pending = nil
	s.ready = nil
	s.pressed = make(map[int64]bool)
	s.keysDown = nil
	s.mu.Unlock()
}

func (s *keyInputState) cache() {
	s.mu.Lock()
	s.ready = append(s.ready, s.pending...)
	changed := len(s.pending) != 0
	s.pending = s.pending[:0]
	if changed {
		s.rebuildKeysDownLocked()
	}
	s.mu.Unlock()
}

func (s *keyInputState) rebuildKeysDownLocked() {
	s.keysDown = s.keysDown[:0]
	for key := range s.pressed {
		s.keysDown = append(s.keysDown, key)
	}
	slices.Sort(s.keysDown)
}

func (s *mouseInputState) drainInput(dst []MouseEvent) ([]MouseEvent, uint8) {
	s.mu.Lock()
	dst = append(dst, s.ready...)
	s.ready = s.ready[:0]
	buttons := s.cachedButtons
	s.mu.Unlock()
	return dst, buttons
}

func (s *mouseInputState) discard() {
	s.mu.Lock()
	s.pending = s.pending[:0]
	s.ready = s.ready[:0]
	s.cachedButtons = s.buttonMask()
	s.mu.Unlock()
}

func (s *mouseInputState) reset() {
	s.mu.Lock()
	for i := range s.buttons {
		s.buttons[i].Store(false)
	}
	s.pending = nil
	s.ready = nil
	s.cachedButtons = 0
	s.mu.Unlock()
}

func (s *mouseInputState) cache() {
	s.mu.Lock()
	s.ready = append(s.ready, s.pending...)
	s.pending = s.pending[:0]
	s.cachedButtons = s.buttonMask()
	s.mu.Unlock()
}

func (s *mouseInputState) buttonMask() uint8 {
	var buttons uint8
	for id := 1; id < len(s.buttons); id++ {
		if s.buttons[id].Load() {
			buttons |= 1 << (id - 1)
		}
	}
	return buttons
}
