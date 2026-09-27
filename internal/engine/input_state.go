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
	mu       sync.Mutex
	pending  []KeyEvent
	ready    []KeyEvent
	pressed  map[int64]bool
	keysDown []int64
}

type mouseInputState struct {
	// mu 保护 pending、ready、cachedButtons 和 captureEnabled。
	// buttons 使用 atomic.Bool，自身可以被 Godot 回调和输入循环并发读写。
	mu sync.Mutex
	// buttons 保存各鼠标按钮当前是否按住的实时状态。
	// 普通实时输入由 inputEventLoop 轮询它（主要比较左键的前后帧状态），
	// 因此普通点击路径不依赖 pending/ready 队列。
	buttons [4]atomic.Bool
	// pending 保存鼠标回调产生的有序按下/抬起边沿，等待 onUpdate() 到达帧边界。
	// 只有 captureEnabled=true（输入录制/回放会话）时才会写入；普通实时模式通常为空。
	pending []MouseEvent
	// ready 保存 cacheMouseEvents() 在帧边界从 pending 转入的边沿事件。
	// 它由输入会话通过 GetMouseInput() 消费；普通 inputEventLoop 不消费这个队列。
	ready []MouseEvent
	// cachedButtons 是 cacheMouseEvents() 在当前帧采样的按住状态位图，
	// 与 ready 一起由 GetMouseInput() 提供给输入录制/回放逻辑。
	cachedButtons uint8
	// captureEnabled 控制是否把鼠标边沿写入 pending；
	// 普通实时输入关闭，输入录制/回放开始时打开，结束时关闭并清空队列。
	captureEnabled bool
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

// GetMouseInput 供输入录制/回放采样使用：取走已缓存的鼠标按钮边沿，并返回本帧按住状态位图。
// 普通实时输入的左键点击不走这里，而由 inputEventLoop 轮询 IsMouseButtonPressed 并比较前后帧状态。
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

// SetMouseEventCaptureEnabled switches ordered mouse-edge capture at a clean boundary.
func SetMouseEventCaptureEnabled(enabled bool) {
	mouseInput.setCaptureEnabled(enabled)
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
// 供实时输入循环轮询；若输入录制/回放采集已开启，再把边沿写入 pending 队列。
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
	if mouseInput.captureEnabled {
		mouseInput.pending = append(mouseInput.pending, MouseEvent{Id: id, IsPressed: pressed})
	}
	mouseInput.mu.Unlock()
}

// cacheKeyEvents 在引擎帧边界把输入回调写入的 pending 键盘边沿转入 ready；
// ready 随后由 inputEventLoop.GetKeyEvents 或输入会话的 GetKeyInput 消费。
func cacheKeyEvents() {
	keyInput.cache()
}

// cacheMouseEvents 在引擎帧边界把录制/回放期间收集的 pending 鼠标边沿转入 ready，
// 同时刷新本帧按住状态快照；ready/快照随后由输入会话的 GetMouseInput 消费。
func cacheMouseEvents() {
	mouseInput.cache()
}

func resetMouseButtonStates() {
	mouseInput.mu.Lock()
	mouseInput.resetButtonsLocked()
	mouseInput.mu.Unlock()
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

func (s *mouseInputState) setCaptureEnabled(enabled bool) {
	s.mu.Lock()
	s.pending = s.pending[:0]
	s.ready = s.ready[:0]
	s.cachedButtons = s.buttonMask()
	s.captureEnabled = enabled
	s.mu.Unlock()
}

func (s *mouseInputState) reset() {
	s.mu.Lock()
	s.resetButtonsLocked()
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

func (s *mouseInputState) resetButtonsLocked() {
	for i := range s.buttons {
		s.buttons[i].Store(false)
	}
}
