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

package event

import (
	"sync"
)

// Sink 是一条长期注册的事件订阅记录。消息广播开始时，scriptEventRegistry
// 会从 BucketIReceive 取得这些记录的稳定快照，再为每个匹配项创建 Thread。
type Sink struct {
	// Owner 是登记脚本的 Game/Sprite 实例，决定目标顺序和 Thread.Obj。
	Owner any
	// Cond 接收本次事件的 matchData 并决定是否触发；nil 表示无条件接收。
	Cond func(any) bool
	// Handler 保存实际处理器。消息事件中它是 *messageEventHandler，后者的
	// run 字段才是用户注册的 OnMsg 函数。
	Handler any
}

type Bucket int

const (
	BucketStart Bucket = iota
	BucketAwake
	BucketKeyPressed
	BucketAnyKeyPressed
	BucketSwipe
	BucketIReceive
	BucketBackdropChanged
	BucketCloned
	BucketTouchStart
	BucketTouching
	BucketTouchEnd
	BucketClick
	BucketTimer
	BucketCondition

	bucketCount
)

// Manager groups sinks by Bucket and tracks the one-time start lifecycle.
// Its dispatch methods treat a nil receiver as a no-op.
type Manager struct {
	mu      sync.RWMutex
	buckets [bucketCount][]Sink

	startFired bool
}

func appendSinkCopy(sinks []Sink, sink Sink) []Sink {
	out := make([]Sink, len(sinks)+1)
	copy(out, sinks)
	out[len(sinks)] = sink
	return out
}

// readOnlySnapshot 把切片容量限制为当前长度，使调用方 append 时一定分配新数组，
// 不能覆盖 manager 持有的后续存储位置。Manager 的写操作采用复制后发布，所以
// 已返回的快照在解锁后仍保持内容稳定。
func readOnlySnapshot(sinks []Sink) []Sink {
	if len(sinks) == 0 {
		return sinks
	}
	return sinks[:len(sinks):len(sinks)]
}

func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.buckets {
		m.buckets[i] = nil
	}
	m.startFired = false
}

func (m *Manager) DeleteOwner(owner any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.buckets {
		m.buckets[i] = deleteOwnerCopy(m.buckets[i], owner)
	}
}

func deleteOwnerCopy(sinks []Sink, owner any) []Sink {
	if len(sinks) == 0 {
		return nil
	}

	firstMatch := -1
	for i, sink := range sinks {
		if sink.Owner == owner {
			firstMatch = i
			break
		}
	}
	if firstMatch < 0 {
		return sinks
	}

	out := make([]Sink, 0, len(sinks)-1)
	out = append(out, sinks[:firstMatch]...)
	for _, sink := range sinks[firstMatch+1:] {
		if sink.Owner != owner {
			out = append(out, sink)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Manager) Add(bucket Bucket, sink Sink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buckets[bucket] = appendSinkCopy(m.buckets[bucket], sink)
}

func (m *Manager) AddStart(sink Sink) {
	m.Add(BucketStart, sink)
}

func (m *Manager) TryAddStart(sink Sink) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startFired {
		return false
	}
	m.buckets[BucketStart] = appendSinkCopy(m.buckets[BucketStart], sink)
	return true
}

func (m *Manager) AddAwake(sink Sink) {
	m.Add(BucketAwake, sink)
}

func (m *Manager) AddKeyPressed(sink Sink) {
	m.Add(BucketKeyPressed, sink)
}

func (m *Manager) AddAnyKeyPressed(sink Sink) {
	m.Add(BucketAnyKeyPressed, sink)
}

func (m *Manager) AddSwipe(sink Sink) {
	m.Add(BucketSwipe, sink)
}

func (m *Manager) AddIReceive(sink Sink) {
	m.Add(BucketIReceive, sink)
}

func (m *Manager) AddBackdropChanged(sink Sink) {
	m.Add(BucketBackdropChanged, sink)
}

func (m *Manager) AddCloned(sink Sink) {
	m.Add(BucketCloned, sink)
}

func (m *Manager) AddTouchStart(sink Sink) {
	m.Add(BucketTouchStart, sink)
}

func (m *Manager) AddTouching(sink Sink) {
	m.Add(BucketTouching, sink)
}

func (m *Manager) AddTouchEnd(sink Sink) {
	m.Add(BucketTouchEnd, sink)
}

func (m *Manager) AddClick(sink Sink) {
	m.Add(BucketClick, sink)
}

func (m *Manager) AddTimer(sink Sink) {
	m.Add(BucketTimer, sink)
}

func (m *Manager) AddCondition(sink Sink) {
	m.Add(BucketCondition, sink)
}

// Snapshot 返回 bucket 当前全部订阅的稳定只读视图。
//
// 对消息分发而言，这一步冻结本次 Broadcast 能看到的 OnMsg 集合；快照取得后
// 新注册或删除的 sink 不会改变这一批接收者。方法只读取订阅，不执行 Cond，
// 也不创建协程 Thread。
func (m *Manager) Snapshot(bucket Bucket) []Sink {
	// 读锁只保护取得当前已发布切片头；具体匹配会在解锁后进行，避免用户 Cond
	// 执行期间阻塞其他事件的注册和删除。
	m.mu.RLock()
	out := readOnlySnapshot(m.buckets[bucket])
	m.mu.RUnlock()
	return out
}

func (m *Manager) SnapshotStart() []Sink {
	return m.Snapshot(BucketStart)
}

func (m *Manager) SnapshotStartOnce() []Sink {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startFired {
		return nil
	}
	m.startFired = true
	return readOnlySnapshot(m.buckets[BucketStart])
}

func (m *Manager) SnapshotAwake() []Sink {
	return m.Snapshot(BucketAwake)
}

func (m *Manager) SnapshotKeyPressed() []Sink {
	return m.Snapshot(BucketKeyPressed)
}

func (m *Manager) SnapshotAnyKeyPressed() []Sink {
	return m.Snapshot(BucketAnyKeyPressed)
}

func (m *Manager) SnapshotSwipe() []Sink {
	return m.Snapshot(BucketSwipe)
}

func (m *Manager) SnapshotIReceive() []Sink {
	return m.Snapshot(BucketIReceive)
}

func (m *Manager) SnapshotBackdropChanged() []Sink {
	return m.Snapshot(BucketBackdropChanged)
}

func (m *Manager) SnapshotCloned() []Sink {
	return m.Snapshot(BucketCloned)
}

func (m *Manager) SnapshotTouchStart() []Sink {
	return m.Snapshot(BucketTouchStart)
}

func (m *Manager) SnapshotTouching() []Sink {
	return m.Snapshot(BucketTouching)
}

func (m *Manager) SnapshotTouchEnd() []Sink {
	return m.Snapshot(BucketTouchEnd)
}

func (m *Manager) SnapshotClick() []Sink {
	return m.Snapshot(BucketClick)
}

func (m *Manager) SnapshotTimer() []Sink {
	return m.Snapshot(BucketTimer)
}

func (m *Manager) SnapshotCondition() []Sink {
	return m.Snapshot(BucketCondition)
}
