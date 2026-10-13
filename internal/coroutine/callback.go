package coroutine

import "github.com/visualfc/gid"

// callbackScope 标记当前 Go goroutine 正在执行哪类调度器回调。
// 这些回调往往持有 runMu、准入状态或属于被排空对象，不能同步等待自身。
type callbackScope uint8

const (
	callbackExternal   callbackScope = 1 << iota // WaitToDo/主线程 call 等外部函数。
	callbackSetup                                // Task.Setup 登记回调。
	callbackCleanup                              // Thread 退出 cleanup。
	callbackFinalizing                           // Thread 最终 panic 上报阶段。
	callbackShutdown                             // 排空完成后的生命周期清理 call。
	callbackExclusive                            // 持有或等价占用 runMu 的独占回调。
)

// enterCallback 登记当前 goroutine 的回调范围；嵌套回调通过按位或保留外层限制。
func (p *Coroutines) enterCallback(scope callbackScope) (id uint64, previous callbackScope) {
	id = gid.Get()
	if value, ok := p.callbacks.Load(id); ok {
		previous = value.(callbackScope)
	}
	p.callbacks.Store(id, previous|scope)
	return
}

// leaveCallback 恢复进入嵌套回调前的范围；最外层退出时删除 goroutine 记录。
func (p *Coroutines) leaveCallback(id uint64, previous callbackScope) {
	if previous == 0 {
		p.callbacks.Delete(id)
	} else {
		p.callbacks.Store(id, previous)
	}
}

// currentCallback 返回调用方 goroutine 当前叠加的回调范围。
func (p *Coroutines) currentCallback() callbackScope {
	if value, ok := p.callbacks.Load(gid.Get()); ok {
		return value.(callbackScope)
	}
	return 0
}

// requireDrainCaller 禁止运行时回调同步排空包含自身的生命周期集合。
func (p *Coroutines) requireDrainCaller() {
	if p.currentCallback() != 0 {
		panic(ErrReentrantWait)
	}
}

// waitOutsideScript 供普通 Go goroutine 等待完成 channel。
// 独占回调若等待需要自己释放的脚本状态会死锁，因此直接拒绝。
func (p *Coroutines) waitOutsideScript(done <-chan struct{}) {
	select {
	case <-done:
		return
	default:
	}
	if p.currentCallback()&callbackExclusive != 0 {
		panic(ErrReentrantWait)
	}
	<-done
}
