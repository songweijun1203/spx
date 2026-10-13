//go:build js || pure_engine

package coroutine

// Web/pure 平台不需要 native 主线程任务泵；等待时由宿主回调继续推进。
const hasMainThreadQueue = false
