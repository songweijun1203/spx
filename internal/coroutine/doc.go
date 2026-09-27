// Package coroutine 为 SPX 脚本提供协作式调度器。
//
// 这里的 Thread 不是独立的语言级协程实现：每个 Thread 由一个 Go goroutine 承载，
// 但所有脚本 goroutine 共享 runMu，因此同一时刻最多只有一个脚本执行片段可以运行。
// 脚本调用 Yield、Wait、WaitNextFrame 等方法时释放 runMu；恢复后必须重新取得 runMu，
// 才能继续执行用户代码。这使业务脚本保持串行语义，同时仍能等待计时器、通道和主线程。
//
// 一帧中的核心调用关系是：
//
//	Godot 帧回调
//	  -> internal/engine.onUpdate
//	  -> Coroutines.Update
//	  -> 从 currentJobs 取出到期 WaitJob
//	  -> markRunnableAndResume
//	  -> Thread 从 Yield 恢复并竞争 runMu
//	  -> 脚本执行到下一次 Yield 或结束
//	  -> Update 在没有可运行脚本和当前帧任务后返回
//
// 需要区分三层状态：
//  1. current：当前持有 runMu、正在执行用户脚本的 Thread；
//  2. runnableThreads：调度器判断 Update 是否仍需等待的逻辑可运行集合；
//  3. suspendState：某个 Thread 的底层 goroutine 是否已进入 Cond.Wait，以及提前到达的
//     Resume 是否需要留存。这三者服务于不同竞态，不能合并为一个状态字段。
//
// 文件职责：
//   - manager.go：调度器共享状态和锁顺序；
//   - lifecycle.go/thread.go：Thread 创建、运行、退出及对象状态；
//   - scheduler.go：Yield/Resume 和唯一脚本执行权；
//   - update.go/frame.go/wait.go：帧驱动等待任务与恢复条件；
//   - mainthread.go：脚本与 Godot 主线程之间的同步调用；
//   - join.go/latch.go/waiters.go/batch.go：协程间等待和批量启动；
//   - handler.go：同一事件处理器重入时的策略；
//   - shutdown.go/callback.go：停止、排空和重入保护；
//   - queue.go/stats.go：内部任务队列和性能统计。
package coroutine
