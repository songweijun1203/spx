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

import "sync"

// node 是双向链表中的一个内部节点。Queue 复用节点对象，以减少每帧调度时
// 频繁创建 WaitJob 队列节点带来的内存分配和 GC 压力。
type node[T any] struct {
	// value 保存当前节点承载的队列元素。
	value T
	// prev 指向前一个节点；队头节点的 prev 为 nil。
	prev *node[T]
	// next 指向后一个节点；队尾节点的 next 为 nil。
	next *node[T]
}

// Queue 是线程安全的泛型双端队列，零值可以直接使用。
//
// 协程调度器用它保存 currentJobs、deferredJobs 和 loopJobs。队列内部使用
// 双向链表，因此从队头/队尾插入和移除都是 O(1)；Move 可以直接拼接链表，
// 无需逐个复制 WaitJob。
type Queue[T any] struct {
	// mu 保护下面所有链表指针、元素数量和节点池初始化操作。
	mu sync.Mutex
	// head 指向队头；队列为空时为 nil。
	head *node[T]
	// tail 指向队尾；队列为空时为 nil。
	tail *node[T]
	// count 是当前元素数量，用于 O(1) 返回 Count 和判空。
	count int
	// pool 回收已经出队的 node，避免调度热路径反复分配链表节点。
	pool sync.Pool
}

// NewQueue 创建一个空队列，并预先配置节点池的构造函数。
// Queue 的零值同样可用；ensurePool 会在首次需要节点时补上构造函数。
func NewQueue[T any]() *Queue[T] {
	q := &Queue[T]{}
	q.pool.New = func() any {
		return new(node[T])
	}
	return q
}

// Move 把 src 的全部节点整体追加到 q 的队尾，并清空 src。
//
// 它移动的是链表所有权，不复制元素，所以复杂度为 O(1)。同时移动两个队列
// 时固定先锁 q、再锁 src；调用方若并发做反方向移动，必须保证统一方向，
// 否则可能形成 q 等 src、src 等 q 的锁顺序死锁。
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
		src.head.prev = q.tail
		q.tail = src.tail
	}
	q.count += src.count

	src.head = nil
	src.tail = nil
	src.count = 0
}

// Count 返回当前队列元素数量。读取也要加锁，因为其他 goroutine 可能入队。
func (q *Queue[T]) Count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count
}

// PushBack 把 value 追加到队尾，是普通 WaitJob 最常用的入队方式。
func (q *Queue[T]) PushBack(value T) {
	q.mu.Lock()
	defer q.mu.Unlock()

	newNode := q.acquireNode(value)
	if q.count == 0 {
		q.head = newNode
		q.tail = newNode
	} else {
		newNode.prev = q.tail
		q.tail.next = newNode
		q.tail = newNode
	}
	q.count++
}

// PushFront 把 value 插入队头。主线程任务使用它获得比普通等待任务更高的
// 处理优先级，从而避免脚本等待引擎调用时形成死锁。
func (q *Queue[T]) PushFront(value T) {
	q.mu.Lock()
	defer q.mu.Unlock()

	newNode := q.acquireNode(value)
	if q.count == 0 {
		q.head = newNode
		q.tail = newNode
	} else {
		newNode.next = q.head
		q.head.prev = newNode
		q.head = newNode
	}
	q.count++
}

// PopFront 移除并返回队头元素。空队列调用属于调度器内部错误，会 panic。
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
	} else {
		q.head.prev = nil
	}

	q.releaseNode(n)
	return value
}

// PopBack 移除并返回队尾元素。空队列调用属于调度器内部错误，会 panic。
func (q *Queue[T]) PopBack() T {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.count == 0 {
		panic("queue is empty")
	}

	n := q.tail
	value := n.value
	q.tail = n.prev
	q.count--

	if q.count == 0 {
		q.head = nil
	} else {
		q.tail.next = nil
	}

	q.releaseNode(n)
	return value
}

// ensurePool 确保节点池拥有 New 函数，使零值 Queue 也能延迟初始化并使用。
func (q *Queue[T]) ensurePool() {
	if q.pool.New == nil {
		q.pool.New = func() any {
			return new(node[T])
		}
	}
}

// acquireNode 从对象池取得节点，写入新值并清除可能残留的链表指针。
func (q *Queue[T]) acquireNode(value T) *node[T] {
	q.ensurePool()
	n := q.pool.Get().(*node[T])
	n.value = value
	n.prev = nil
	n.next = nil
	return n
}

// releaseNode 清除节点持有的值和链接后放回对象池。清零 value 很重要，
// 否则池中的节点可能长期引用 Thread/WaitJob，妨碍垃圾回收。
func (q *Queue[T]) releaseNode(n *node[T]) {
	var zero T
	n.value = zero
	n.prev = nil
	n.next = nil
	q.ensurePool()
	q.pool.Put(n)
}
