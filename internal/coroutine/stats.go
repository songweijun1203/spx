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

// UpdateJobsStats 保存一次 Coroutines.Update 调用的耗时和任务计数。
// 所有 float64 时间字段的单位都是毫秒；这些统计只用于诊断调度性能，不参与
// 调度决策。启用 GC 统计会增加额外开销，因此由 SetPerfDebug 单独控制。
type UpdateJobsStats struct {
	// InitTime 是读取帧号、关卡时间和建立本轮 updateState 的耗时。
	InitTime float64
	// LoopTime 是 runUpdateLoop 主循环的总耗时，包含处理、等待和调度开销。
	LoopTime float64
	// MoveTime 是帧末把 loopJobs/deferredJobs 搬回 currentJobs 的耗时。
	MoveTime float64
	// WaitTime 是 Update 等待 runnable Thread 改变状态所消耗的时间。
	WaitTime float64
	// TaskProcessing 是从队列取出并分类/执行 WaitJob 的累计耗时。
	TaskProcessing float64
	// GCPauses 是这次 Update 期间观察到的 Go GC 暂停总时长。
	GCPauses float64
	// ExternalTime 是 TotalTime 中未被 Init/Loop/Move 三项覆盖的时间，包括
	// Go 运行时调度、函数调用以及统计本身的开销。
	ExternalTime float64
	// TotalTime 是整个 Update 从进入到完成统计的墙钟耗时。
	TotalTime float64
	// TimeDifference 当前与 ExternalTime 使用相同计算，保留作诊断兼容字段。
	TimeDifference float64
	// TaskCounts 是本次 Update 从 currentJobs 取出的任务总数。
	TaskCounts int
	// WaitFrameCount 是本次成功恢复的 waitTypeFrame 任务数量。
	WaitFrameCount int
	// WaitMainCount 是本次执行的 waitTypeMainThread 回调数量。
	WaitMainCount int
	// NextCount 是 Update 结束时被推迟到后续 Update 的任务数量。
	NextCount int
	// GCCount 是 Update 期间发生的垃圾回收轮数。
	GCCount int
	// GCStatsEnabled 表示本次是否实际采集 GCCount 和 GCPauses。
	GCStatsEnabled bool
	// LoopIterations 是 runUpdateLoop 顶层循环迭代次数，包含 retry/complete 检查。
	LoopIterations int
}

// GetLastUpdateStats 返回该协程管理器最近一次 Update 的统计快照。
// 通过读锁复制结构体，调用者不会观察到正在写入的半成品；只有
// GCStatsEnabled 为 true 时，GCCount 和 GCPauses 才有意义。
func (p *Coroutines) GetLastUpdateStats() UpdateJobsStats {
	p.statsMu.RLock()
	stats := p.lastUpdateStats
	p.statsMu.RUnlock()
	return stats
}
