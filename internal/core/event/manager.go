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

type Sink struct {
	Owner   any
	Cond    func(any) bool
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
// Published slice elements are never changed: registration appends after the
// existing prefix, deletion copies survivors, and Reset drops slice references.
type Manager struct {
	mu      sync.RWMutex
	buckets [bucketCount][]Sink

	startFired bool
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

func (m *Manager) Add(bucket Bucket, sink Sink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buckets[bucket] = append(m.buckets[bucket], sink)
}

// TryAddStart 只在一次性启动快照尚未生成时接受 OnStart 注册。
//
// 直接调用方：scriptEventBindings.OnStart；总体流程调用方：Game/精灵 Main 的事件注册，
// 以及运行时克隆重跑 Main。返回 false 表示 OnStart 已开始派发，调用方不能补入本批次。
func (m *Manager) TryAddStart(sink Sink) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startFired {
		return false
	}
	m.buckets[BucketStart] = append(m.buckets[BucketStart], sink)
	return true
}

// Snapshot returns a shallow, read-only view of the current registrations.
// Callers must not assign its elements. The view remains stable across manager
// writes, and its capacity is limited so appending cannot change the bucket.
// Objects referenced by a Sink, including its Handler, are not made immutable.
func (m *Manager) Snapshot(bucket Bucket) []Sink {
	m.mu.RLock()
	out := readOnlySnapshot(m.buckets[bucket])
	m.mu.RUnlock()
	return out
}

// SnapshotStartOnce 关闭后续 OnStart 注册，并返回唯一一次启动处理器快照。
//
// 直接调用方：Game.takeStartSinks；总体流程调用方：bootstrap 完成后的项目 OnStart
// 派发。startFired 在返回快照前置为 true，所以 OnStart 处理器内部创建的运行时克隆
// 即使重跑 Main，也无法把新 OnStart 插入正在派发的启动批次。
// 返回切片遵循与 Snapshot 相同的只读、浅快照约定。
func (m *Manager) SnapshotStartOnce() []Sink {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startFired {
		return nil
	}
	m.startFired = true
	return readOnlySnapshot(m.buckets[BucketStart])
}

func readOnlySnapshot(sinks []Sink) []Sink {
	return sinks[:len(sinks):len(sinks)]
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
