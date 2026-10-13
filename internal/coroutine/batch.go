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

// BatchMode 决定 StartBatch 登记任务后，调用方需要等待到哪个生命周期阶段。
type BatchMode uint8

const (
	_ BatchMode = iota
	// BatchAsync 只登记并放行任务，不等待处理器取得执行权。
	BatchAsync
	// BatchWaitFirstSlice 等到每个任务至少完成首个执行片段：任务已经结束，或者已经
	// 主动 Yield/Wait 并把调度权交回。克隆的 OnCloned 用它完成“首段初始化”屏障。
	BatchWaitFirstSlice
	// BatchWaitDone 等待批次中的全部任务彻底结束。
	BatchWaitDone
)

// StartBatch 先按顺序登记整批任务，再根据 mode 决定调用方等待到哪个阶段。
//
// 直接调用方：脚本事件派发和若干调度器内部批处理；总体流程调用方包括输入、消息、
// OnStart、OnCloned 等事件。等待模式要求调用方处于受管协程中；Setup 可以取消任务，
// 但不能反过来等待该任务，否则会和批次登记屏障形成死锁。
func (p *Coroutines) StartBatch(tasks []Task, mode BatchMode) []Thread {
	if mode != BatchAsync && mode != BatchWaitFirstSlice && mode != BatchWaitDone {
		panic("coroutine: invalid batch mode")
	}
	if len(tasks) == 0 {
		return nil
	}

	threads, progress := p.registerBatch(tasks)
	relayBatchProgress(threads, progress[1:])
	progress[0].Open()
	switch mode {
	case BatchWaitFirstSlice:
		progress[len(tasks)].Wait()
	case BatchWaitDone:
		p.JoinAll(threads)
	}
	return threads
}

// registerBatch 先创建全部 Thread，并用一组 Latch 串成严格的启动顺序。
// 第 i 个任务只有在 progress[i] 打开后才进入用户 Run；它开始首段执行前就打开
// progress[i+1]，但 runMu 保证后续任务只有在前一个任务 Yield/结束后才能真正运行。
func (p *Coroutines) registerBatch(tasks []Task) ([]Thread, []*Latch) {
	progress := newLatchSet(p, len(tasks)+1)
	threads := make([]Thread, len(tasks))
	batchCreated := false
	defer func() {
		if !batchCreated {
			for _, thread := range threads {
				p.Stop(thread)
			}
		}
	}()
	admission := p.captureAdmission()
	for i, task := range tasks {
		current, next := progress[i], progress[i+1]
		if setup := task.Setup; setup != nil {
			// 调用 Setup 前先发布槽位，让回调观察到的登记顺序与 tasks 一致。
			task.Setup = func(thread Thread) func() {
				threads[i] = thread
				return setup(thread)
			}
		}
		run := task.Run
		task.Run = func(thread Thread) {
			defer next.Open()
			current.Wait()
			next.Open()
			run(thread)
		}
		threads[i] = p.createThread(admission, task)
	}
	batchCreated = true
	return threads, progress
}

// newLatchSet 创建 size 个彼此独立的批次进度闩。
func newLatchSet(p *Coroutines, size int) []*Latch {
	latches := make([]*Latch, size)
	for i := range latches {
		latches[i] = p.NewLatch()
	}
	return latches
}

// relayBatchProgress 处理尚未取得首段执行机会就被取消的任务。
// 它按顺序补开对应进度闩，防止后续任务越过仍未完成的前缀。
func relayBatchProgress(threads []Thread, progress []*Latch) {
	go func() {
		for i, thread := range threads {
			select {
			case <-progress[i].Done():
			case <-thread.Context().Done():
				progress[i].Open()
			}
		}
	}()
}
