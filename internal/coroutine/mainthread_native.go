//go:build !js && !pure_engine

package coroutine

// native 平台上的 Godot 节点操作必须排队回引擎主线程执行。
const hasMainThreadQueue = true
