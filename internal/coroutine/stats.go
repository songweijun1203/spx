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

// UpdateJobsStats 记录最近一次 Coroutines.Update 的耗时分解和任务计数。
// 所有 duration 字段单位均为毫秒。
type UpdateJobsStats struct {
	InitTime       float64 // 准备帧快照和预算的耗时。
	LoopTime       float64 // Update 主循环总耗时。
	MoveTime       float64 // 合并、排序和提升延期任务的耗时。
	WaitTime       float64 // 等待 runnable Thread 让出/结束的耗时。
	TaskProcessing float64 // 取出和处理 WaitJob 的耗时。
	GCPauses       float64 // 本次 Update 期间观察到的 GC 暂停。
	ExternalTime   float64 // 未归类耗时，包括 Go runtime 调度开销。
	TotalTime      float64 // Update 总耗时。
	TaskCounts     int     // 实际从 currentJobs 取出的任务数。
	WaitFrameCount int     // 本轮恢复的 waitTypeFrame 数量。
	WaitMainCount  int     // 本轮执行的主线程任务数。
	NextCount      int     // 循环结束时推迟到后续 Update 的任务数。
	GCCount        int     // 本次 Update 期间 GC 次数。
	GCStatsEnabled bool    // 是否启用了 GCCount/GCPauses 采集。
	LoopIterations int     // Update 主循环迭代次数。
}

// GetLastUpdateStats 返回当前管理器最近一次 Update 的统计快照。
// 只有 GCStatsEnabled=true 时，GCCount 和 GCPauses 才有意义。
func (p *Coroutines) GetLastUpdateStats() UpdateJobsStats {
	p.statsMu.RLock()
	stats := p.lastUpdateStats
	p.statsMu.RUnlock()
	return stats
}
