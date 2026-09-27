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

package coroutine

import (
	"slices"
	"sync"
)

// node 是 Queue 使用的单向链表节点；弹出后会回收到共享 sync.Pool。
type node[T any] struct {
	value T
	next  *node[T]
}

// Queue 是支持首尾插入的线程安全链表队列，零值可直接使用。
//
// Coroutines 用它保存 WaitJob。链表使 current/deferred/round 队列可以 O(1) 整体拼接；
// pool 复用高频任务节点，sortBuffer 复用稳定排序的临时切片。
type Queue[T any] struct {
	mu    sync.Mutex
	head  *node[T]
	tail  *node[T]
	count int
	pool  *sync.Pool

	sortBuffer []T
}

// Move 把 src 的全部节点 O(1) 追加到 q 尾部，并清空 src。
// 两个 goroutine 若需要在同一对队列间并发 Move，必须保持一致方向以免锁顺序死锁。
func (q *Queue[T]) Move(src *Queue[T]) {
	if q == src {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	src.mu.Lock()
	defer src.mu.Unlock()

	if src.count == 0 {
		return
	}

	if q.count == 0 {
		q.head = src.head
		q.tail = src.tail
	} else {
		q.tail.next = src.head
		q.tail = src.tail
	}
	q.count += src.count

	src.head = nil
	src.tail = nil
	src.count = 0
}

// Count 返回当前元素数量。
func (q *Queue[T]) Count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count
}

// PeekFront 查看队首但不移除；空队列返回 ok=false。
func (q *Queue[T]) PeekFront() (value T, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.head == nil {
		return value, false
	}
	return q.head.value, true
}

// SortStable 就地稳定排序节点中的值；链表节点和拓扑保持不变。
func (q *Queue[T]) SortStable(compare func(T, T) int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.count < 2 {
		return
	}
	values := slices.Grow(q.sortBuffer[:0], q.count)
	for n := q.head; n != nil; n = n.next {
		values = append(values, n.value)
	}
	q.sortBuffer = values
	defer clear(values)
	slices.SortStableFunc(values, compare)
	n := q.head
	for _, value := range values {
		n.value = value
		n = n.next
	}
}

// Any 判断是否至少一个队列值满足 match。
func (q *Queue[T]) Any(match func(T) bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for node := q.head; node != nil; node = node.next {
		if match(node.value) {
			return true
		}
	}
	return false
}

// PushBack 把值加入队尾。
func (q *Queue[T]) PushBack(value T) {
	q.mu.Lock()
	defer q.mu.Unlock()

	newNode := q.acquireNode(value)
	if q.count == 0 {
		q.head = newNode
		q.tail = newNode
	} else {
		q.tail.next = newNode
		q.tail = newNode
	}
	q.count++
}

// PushFront 把值加入队首；主线程优先任务使用该入口。
func (q *Queue[T]) PushFront(value T) {
	q.mu.Lock()
	defer q.mu.Unlock()

	newNode := q.acquireNode(value)
	if q.count == 0 {
		q.head = newNode
		q.tail = newNode
	} else {
		newNode.next = q.head
		q.head = newNode
	}
	q.count++
}

// PopFront 移除并返回队首；空队列调用属于内部编程错误，会 panic。
func (q *Queue[T]) PopFront() T {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.count == 0 {
		panic("queue is empty")
	}

	n := q.head
	value := n.value
	q.head = n.next
	q.count--

	if q.count == 0 {
		q.tail = nil
	}

	q.releaseNode(n)
	return value
}

// NewQueue 创建带独立节点池的空队列。
func NewQueue[T any]() *Queue[T] {
	q := &Queue[T]{}
	q.ensurePool()
	return q
}

// ensurePool 使 Queue 零值也能延迟初始化节点池。
func (q *Queue[T]) ensurePool() {
	if q.pool == nil {
		q.pool = &sync.Pool{New: func() any {
			return new(node[T])
		}}
	}
}

// acquireNode 从池中取得并初始化节点。
func (q *Queue[T]) acquireNode(value T) *node[T] {
	q.ensurePool()
	n := q.pool.Get().(*node[T])
	n.value = value
	n.next = nil
	return n
}

// releaseNode 清除泛型值引用后回收节点，避免池长期持有业务对象。
func (q *Queue[T]) releaseNode(n *node[T]) {
	var zero T
	n.value = zero
	n.next = nil
	q.ensurePool()
	q.pool.Put(n)
}
