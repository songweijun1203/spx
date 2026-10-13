package coroutine

import "sync"

// waiterSet 管理一次性等待者集合；零值表示仍开放登记。
// 用于 Join、JoinYieldedOrDone 和 Latch，不在关闭后重新开放。
type waiterSet struct {
	mu      sync.Mutex
	closed  bool
	threads map[Thread]struct{}
}

// waitOn 原子发布阻塞状态和等待者登记，再让出当前脚本执行权。
//
// 锁顺序固定为 schedulerMu -> waiterSet.mu，防止 close 与登记交错而丢失唤醒。
// 若集合已经关闭，则恢复 runnable 并且不 Yield；取消导致 Yield panic 时 defer 仍会注销。
func (p *Coroutines) waitOn(me Thread, waiters *waiterSet) {
	p.schedulerMu.Lock()
	p.setThreadStateLocked(me, threadBlocked)
	registered := waiters.add(me)
	if !registered {
		p.setThreadStateLocked(me, threadRunnable)
	}
	p.schedulerCond.Signal()
	p.schedulerMu.Unlock()

	if registered {
		defer waiters.remove(me)
		p.Yield(me)
	}
}

// add 登记一个 Thread；集合已经关闭时返回 false，表示条件早已满足。
func (p *waiterSet) add(thread Thread) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	if p.threads == nil {
		p.threads = make(map[Thread]struct{})
	}
	p.threads[thread] = struct{}{}
	return true
}

// remove 清理取消或已经恢复的等待者。
func (p *waiterSet) remove(thread Thread) {
	p.mu.Lock()
	delete(p.threads, thread)
	if len(p.threads) == 0 {
		p.threads = nil
	}
	p.mu.Unlock()
}

// close 永久关闭集合，并把等待者所有权转交调用方；调用方必须在锁外逐个恢复。
// done 非 nil 时同步关闭外部等待 channel；nil 用于 channel 已由其他退出步骤发布的场景。
func (p *waiterSet) close(done chan struct{}) map[Thread]struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if done != nil {
		close(done)
	}
	waiters := p.threads
	p.threads = nil
	return waiters
}
