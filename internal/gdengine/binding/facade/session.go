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

package facade

import "sync"

// LinkSession 是一次“游戏运行时 <-> 平台绑定”的公共生命周期句柄。
//
// 它不是网络连接，也不代表某个 Godot 节点；它把平台特有的启动函数和清理函数
// 绑定到同一个会话，保证同一会话最多启动一次、清理一次。
// 平台：公共封装；Native、Web、pure_engine 都通过它统一会话生命周期。
type LinkSession struct {
	// run 启动平台后端。参数回调表示“后端已完成启动状态捕获，公共层可以继续运行”。
	// Native/pure_engine 会立即调用回调并返回；Web 会调用回调后阻塞等待退出。
	run func(ready func())
	// unlink 执行平台特有的清理。Native/pure_engine 当前无需额外清理，因此可以为 nil；
	// Web 使用它关闭 exit 通道，使 webffi.LinkSession.Run() 返回。
	unlink func()
	// mu 串行化 Run 与 Unlink，防止清理动作越过尚未完成的启动状态捕获。
	mu sync.Mutex
	// started/closed 分别保证 run 和 unlink 在同一会话内最多执行一次。
	started bool
	closed  bool
}

// Run 只允许底层 run 执行一次，并保证 ready 在启动状态被安全记录后调用。
// 直接上级：internal/gdengine.(*LinkSession).Run()。
func (s *LinkSession) Run(ready func()) {
	s.mu.Lock()
	if s.closed || s.started {
		s.mu.Unlock()
		if ready != nil {
			ready()
		}
		return
	}
	s.started = true
	// Web 的底层 run 会一直阻塞到 Unlink。它调用内部 ready 时，说明启动状态已经
	// 捕获完成，此时必须提前释放锁，让另一个 goroutine 能进入 Unlink 并关闭 Web 会话；
	// defer 是底层 run 未调用 ready 就直接返回时的兜底解锁。
	releaseStartup := sync.OnceFunc(s.mu.Unlock)
	defer releaseStartup()
	s.run(func() {
		releaseStartup()
		if ready != nil {
			ready()
		}
	})
}

// Unlink 只执行一次平台清理，并等待正在进行的启动状态捕获完成。
// 直接上级：internal/gdengine.(*LinkSession).Unlink()；Web 会继续调用 webffi.Unlink()。
func (s *LinkSession) Unlink() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.unlink != nil {
		s.unlink()
	}
}

// newLinkSession 把平台特有的 run/unlink 函数包装为统一会话。
// 直接上级：三个 facade.LinkFFI() 平台实现。
// 返回值随后由 gdengine.PrepareLink() 包装并保存到当前 gameBinding 中。
func newLinkSession(run func(func()), unlink func()) *LinkSession {
	return &LinkSession{run: run, unlink: unlink}
}
