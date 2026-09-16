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

package spx

import (
	"slices"
	"sync"
	"sync/atomic"

	"github.com/goplus/spbase/mathf"
	coreevent "github.com/goplus/spx/v3/internal/core/event"
	coreruntime "github.com/goplus/spx/v3/internal/core/runtime"
	"github.com/goplus/spx/v3/internal/coroutine"
	"github.com/goplus/spx/v3/internal/engine"
	spxlog "github.com/goplus/spx/v3/internal/log"
	itime "github.com/goplus/spx/v3/internal/time"
)

const (
	clickTimerGlobal = -1
	clickTimerStage  = 0
)

// Event Bindings
type eventSink = coreevent.Sink

type scriptEventBindings struct {
	*scriptEventRegistry
	pthis threadObj
}

// scriptEventRegistry 是脚本事件系统的运行时注册表和统一分发入口。
//
// 注册阶段，scriptEventBindings 把 OnStart、OnMsg、OnCloned 等用户处理函数
// 包装成 eventSink，按事件类型存入 manager。事件发生后，本类型的方法负责取得
// sink 快照、按 Scratch 目标顺序排列、构造 scriptEventDispatch，并最终通过
// coroutine.StartBatch 为每个匹配的 sink 创建独立 Thread。
type scriptEventRegistry struct {
	// manager 按 Bucket 保存全部 eventSink，并提供并发安全的注册、删除和快照。
	// scriptEventRegistry 负责在快照之上补充 SPX 的目标排序和协程分发语义。
	manager coreevent.Manager
	// messageHandlerFrames 记录当前正在执行消息 handler 的 Thread 及其开始帧号。
	// shouldDeferMessageReceivers 用它识别“消息 handler 在开始的同一帧内又进行
	// 非等待广播”，并把新接收者推迟到下一帧，避免同帧递归扩散。
	messageHandlerFrames sync.Map // map[coroutine.Thread]int64
	// stopAllEpoch 是全局 Stop(AllStop) 的单调递增代次。开始事件批次会在创建
	// Thread 前保存基线；若某个 OnStart 执行前代次已改变，就让该 Thread 在
	// 第一次 Yield 时停止，同时仍允许当前开始事件快照按顺序完成首次执行片段。
	stopAllEpoch atomic.Uint64
	// pendingStartThreads 保存已经注册、但尚未进入 OnStart task.Run 的 Thread。
	// Stop(AllStop) 不立即取消这些 Thread，以免破坏 StartBatch 的接力顺序；
	// dispatchStartEventBatch 会在它们真正开始时结合 stopAllEpoch 安排停止。
	pendingStartThreads sync.Map // map[coroutine.Thread]struct{}
	// pendingConditions 保存本帧条件采样阶段已经匹配、等待分发的 OnCond sink。
	// 它只由引擎帧线程访问；采样和执行分成两步，确保条件统一观察同一状态快照。
	pendingConditions []eventSink // 仅由引擎帧线程访问
}

// messageEventHandler 包装一段用户注册的消息脚本，并跟踪它当前的活动 Thread。
// 同一段“当收到消息”脚本被再次触发时，start 会用新 Thread 替换并停止旧实例。
type messageEventHandler struct {
	// mu 保护 active 的替换和条件清理，run 在注册完成后保持只读。
	mu sync.Mutex
	// active 是这段消息脚本当前尚未结束的执行实例；没有活动实例时为 nil。
	active coroutine.Thread
	// run 是生成代码注册的真实用户 handler，参数依次为消息名和附加数据。
	run func(string, any)
}

type startEventDispatcher struct{}

// Click Dispatch
type clicker interface {
	threadObj
	doWhenClick(this threadObj)
	getProxy() *engine.Sprite
	Visible() bool
}

func (p *scriptEventBindings) OnStart(onStart func()) {
	// 这里只登记“绿旗开始时要调用哪个函数”。sink 保存 owner 和 handler，
	// 并没有创建 coroutine.Thread，也不会立即调用 onStart。真正的 Thread
	// 要等 eventStart 到达、完成匹配和排序后才由 StartBatch 创建。
	sink := coreevent.NewSink(p.pthis, onStart)
	if p.scriptEventRegistry.manager.TryAddStart(sink) {
		return
	}
	if sprite, ok := sink.Owner.(*SpriteImpl); ok && sprite.spriteState.Cloned {
		return
	}
	spxlog.Warn("Event: ignoring late OnStart registration for %s", nameOf(sink.Owner))
}

func (p *scriptEventBindings) OnClick(onClick func()) {
	pthis := p.pthis
	p.scriptEventRegistry.manager.AddClick(coreevent.NewSink(pthis, onClick, coreevent.MatchOwner(pthis)))
}

func (p *scriptEventBindings) OnAnyKey(onKey func(key Key)) {
	p.registerKeyHandler([]Key{KeyAny}, onKey)
}

func (p *scriptEventBindings) OnTimer(time float64, call func()) {
	itime.RegisterTimer(time)
	p.scriptEventRegistry.manager.AddTimer(coreevent.NewSink(
		p.pthis,
		coreevent.TapVoid1(call, coreevent.If1(isDebugEventEnabled, func(float64) {
			spxlog.Debug("OnTimer: %s", nameOf(p.pthis))
		})),
		coreevent.MatchApproxFloat(time, 0.001),
	))
}

func (p *scriptEventBindings) OnKey__0(key Key, onKey func()) {
	handler := coreevent.TapVoid1(onKey, coreevent.If1(isDebugEventEnabled, func(Key) {
		spxlog.Debug("OnKey: %v, %s", key, nameOf(p.pthis))
	}))
	p.registerKeyHandler([]Key{key}, handler)
}

func (p *scriptEventBindings) OnSwipe__0(direction Direction, onSwipe func()) {
	p.scriptEventRegistry.manager.AddSwipe(coreevent.NewSink(
		p.pthis,
		coreevent.TapVoid1(onSwipe, coreevent.If1(isDebugEventEnabled, func(Direction) {
			spxlog.Debug("OnSwipe: %v, %s", direction, nameOf(p.pthis))
		})),
		coreevent.MatchValue(direction),
	))
}

func (p *scriptEventBindings) OnKey__1(keys []Key, onKey func(Key)) {
	handler := coreevent.Tap1(onKey, coreevent.If1(isDebugEventEnabled, func(key Key) {
		spxlog.Debug("OnKey: %v, %s", keys, nameOf(p.pthis))
	}))
	p.registerKeyHandler(keys, handler)
}

func (p *scriptEventBindings) OnKey__2(keys []Key, onKey func()) {
	p.OnKey__1(keys, coreevent.Ignore1[Key](onKey))
}

func (p *scriptEventBindings) OnMsg__0(onMsg func(msg MsgName, data any)) {
	p.registerMessageHandler(onMsg)
}

func (p *scriptEventBindings) OnMsg__1(msg MsgName, onMsg func()) {
	p.registerMessageHandler(
		coreevent.TapVoid2(onMsg, coreevent.If2(isDebugEventEnabled, func(msg string, data any) {
			spxlog.Debug("OnMsg: %s, %s", msg, nameOf(p.pthis))
		})),
		coreevent.MatchValue(msg),
	)
}

func (p *scriptEventBindings) OnBackdrop__0(onBackdrop func(name BackdropName)) {
	p.scriptEventRegistry.manager.AddBackdropChanged(coreevent.NewSink(p.pthis, onBackdrop))
}

func (p *scriptEventBindings) OnBackdrop__1(name BackdropName, onBackdrop func()) {
	p.scriptEventRegistry.manager.AddBackdropChanged(coreevent.NewSink(
		p.pthis,
		coreevent.TapVoid1(onBackdrop, coreevent.If1(isDebugEventEnabled, func(name BackdropName) {
			spxlog.Debug("OnBackdrop: %s, %s", name, nameOf(p.pthis))
		})),
		coreevent.MatchValue(name),
	))
}

// Stop stops scripts selected by kind. The explicit receiver enables generated
// direct-call adapters for interpreted scripts.
func (p *Game) Stop(kind StopKind) {
	p.scriptEventBindings.Stop(kind)
}

// Stop stops scripts selected by kind.
func (p *SpriteImpl) Stop(kind StopKind) {
	p.scriptEventBindings.Stop(kind)
}

func (p *scriptEventBindings) Stop(kind StopKind) {
	// Read the receiver before the fast path to preserve nil-receiver behavior.
	owner := p.pthis
	if kind == ThisScript {
		// This signal is scoped by Procedure and never filters other threads.
		gco.AbortThisScript()
		return
	}
	if kind == AllStop {
		p.scriptEventRegistry.stopAllEpoch.Add(1)
		if game := activeGame(); game != nil {
			game.resetGraphicEffectsOnStopAll()
		}
	}

	var current coroutine.Thread
	if gco.IsInCoroutine() {
		current = gco.Current()
	}
	filter, abort := coreevent.ResolveStop(
		kind,
		owner,
		func(obj any) bool { return isSprite(obj) },
		func(obj any) bool { return isGame(obj) },
	)
	if filter != nil {
		gco.StopIf(func(th coroutine.Thread) bool {
			if !filter(th.Obj, th == current) {
				return false
			}
			return kind != AllStop || !p.scriptEventRegistry.isPendingStartThread(th)
		})
	}
	if abort {
		gco.Abort()
	}
}

// Message Broadcast
func (p *Game) Broadcast__0(msg MsgName) {
	p.doBroadcast(msg, nil, false)
}

func (p *Game) Broadcast__1(msg MsgName, data any) {
	p.doBroadcast(msg, data, false)
}

func (p *Game) BroadcastAndWait__0(msg MsgName) {
	p.doBroadcast(msg, nil, true)
}

func (p *Game) BroadcastAndWait__1(msg MsgName, data any) {
	p.doBroadcast(msg, data, true)
}

// start 把本次广播新建的 thread 登记为这个消息脚本当前唯一的活动实例，
// 并返回一个在该实例退出时执行的清理函数。
//
// 同一个 messageEventHandler 对应源代码中的一段“当收到消息”脚本。新广播再次
// 触发同一段脚本时，Scratch 语义要求新实例替换旧实例，因此这里会停止 previous。
func (p *messageEventHandler) start(thread coroutine.Thread) func() {
	// 在锁内完成 active 的读取和替换，保证两个并发分发不会同时认为自己是
	// 唯一活动实例。这里只修改指针，不在持锁期间调用协程停止逻辑。
	p.mu.Lock()
	previous := p.active
	p.active = thread
	p.mu.Unlock()

	// 停止旧实例放在解锁之后，避免 gco.Stop 的取消/唤醒路径与此锁形成锁嵌套。
	if previous != nil && previous != thread {
		gco.Stop(previous)
	}
	// 返回值会被 scriptEventDispatch.task 保存为 cleanup，并在 task.Run 退出时
	// defer 调用。必须再次比较 active：如果期间更新的广播已经换入另一个 Thread，
	// 旧 Thread 的迟到清理不能把新 Thread 的 active 记录误删。
	return func() {
		p.mu.Lock()
		if p.active == thread {
			p.active = nil
		}
		p.mu.Unlock()
	}
}

func (p *scriptEventBindings) init(registry *scriptEventRegistry, this threadObj) {
	p.scriptEventRegistry = registry
	p.pthis = this
}

func (p *scriptEventBindings) initFrom(src *scriptEventBindings, this threadObj) {
	p.scriptEventRegistry = src.scriptEventRegistry
	p.pthis = this
}

func (p *scriptEventBindings) doDeleteClone() {
	p.scriptEventRegistry.manager.DeleteOwner(p.pthis)
}

func (p *scriptEventBindings) doWhenSwipe(direction Direction, target threadObj) {
	p.scriptEventRegistry.doWhenSwipe(direction, target)
}

func (p *scriptEventBindings) onAwake(onAwake func()) {
	pthis := p.pthis
	p.scriptEventRegistry.manager.AddAwake(coreevent.NewSink(p.pthis, onAwake, coreevent.MatchOwnerOrNil(pthis)))
}

func (p *scriptEventBindings) registerKeyHandler(keys []Key, handler func(Key)) {
	if len(keys) == 0 {
		return
	}
	keys = slices.Clone(keys)
	sink := coreevent.NewSink(p.pthis, handler)
	if slices.Contains(keys, KeyAny) {
		p.scriptEventRegistry.manager.AddAnyKeyPressed(sink)
		return
	}
	sink.Cond = coreevent.MatchAnyOf(keys)
	p.scriptEventRegistry.manager.AddKeyPressed(sink)
}

func (p *scriptEventBindings) registerMessageHandler(handler func(string, any), cond ...func(any) bool) {
	// 每段 OnMsg 脚本拥有独立 messageEventHandler，以便分别跟踪它当前的活动
	// Thread。cond 若存在通常由 MatchValue(msg) 生成；没有 cond 表示接收所有消息。
	p.scriptEventRegistry.manager.AddIReceive(coreevent.NewSink(
		p.pthis,
		&messageEventHandler{run: handler},
		cond...,
	))
}

// Scratch clears graphic effects for the stage and every sprite when a
// project-wide stop is triggered, including the `stop all` control block.
func (p *Game) resetGraphicEffectsOnStopAll() {
	p.baseObj.clearGraphicEffects()

	shapes := append([]Shape(nil), p.getAllShapes()...)
	for _, item := range shapes {
		sprite, ok := item.(*SpriteImpl)
		if !ok {
			continue
		}
		sprite.clearGraphicEffects()
	}
}

func (p *Game) pointHitsClickTarget(target clicker, point mathf.Vec2) bool {
	syncSprite := target.getProxy()
	if syncSprite == nil || !target.Visible() {
		return false
	}

	sprite, ok := target.(*SpriteImpl)
	if ok {
		sprite.ensureProxyQueryStateSynced()
		if sprite.isFullyGhosted() {
			return false
		}
	}

	return p.engine().SpriteMgr.CheckCollisionWithPoint(syncSprite.GetId(), point, true)
}

func (p *Game) findClickTarget(point mathf.Vec2) (coreruntime.ClickSelection[clicker, *SpriteImpl], bool) {
	return coreruntime.FindClickTarget(p.getTempShapes(), func(item Shape) (coreruntime.ClickSelection[clicker, *SpriteImpl], bool) {
		o, ok := item.(clicker)
		if !ok {
			return coreruntime.ClickSelection[clicker, *SpriteImpl]{}, false
		}
		if !p.pointHitsClickTarget(o, point) {
			return coreruntime.ClickSelection[clicker, *SpriteImpl]{}, false
		}
		if sprite, ok := o.(*SpriteImpl); ok {
			return coreruntime.ClickSelection[clicker, *SpriteImpl]{Target: o, SwipeTarget: sprite}, true
		}
		return coreruntime.ClickSelection[clicker, *SpriteImpl]{Target: o}, true
	})
}

func (p *Game) doWhenLeftButtonUp(ev *eventLeftButtonUp) {
	p.inputMgr.finishSwipeTracking(ev.Pos)
}

func (p *Game) doWhenLeftButtonDown(ev *eventLeftButtonDown) {
	coreruntime.HandleLeftButtonDown(ev.Pos, coreruntime.ClickDownHooks[clicker, *SpriteImpl, int64]{
		FindTarget: p.findClickTarget,
		BeginSwipe: p.inputMgr.beginSwipeTracking,
		CanTrigger: func(id int64) bool {
			return p.inputMgr.canTriggerClickEvent(id)
		},
		GlobalID: clickTimerGlobal,
		StageID:  clickTimerStage,
		TargetID: func(target clicker) (int64, bool) {
			syncSprite := target.getProxy()
			if syncSprite == nil {
				return 0, false
			}
			return syncSprite.GetId(), true
		},
		DispatchTarget: func(target clicker) {
			target.doWhenClick(target)
		},
		DispatchStage: func() {
			p.scriptEvents.doWhenClick(p)
		},
	})
}

func (p *Game) doBroadcast(msg MsgName, data any, wait bool) {
	if isDebugInstrEnabled() {
		spxlog.Debug("Broadcast: msg=%s, wait=%v", msg, wait)
	}
	p.scriptEvents.doWhenIReceive(msg, data, wait)
}

// Event Routing
// 键盘、鼠标、定时器、开始事件
func (p *Game) handleEvent(ev event) {
	switch e := ev.(type) {
	case *eventLeftButtonUp:
		p.doWhenLeftButtonUp(e)
	case *eventLeftButtonDown:
		p.doWhenLeftButtonDown(e)
	case *eventMouseMove:
		p.inputMgr.onMouseMove(e.Pos)
	case *eventKeyDown:
		// Note: key-up callbacks are not part of the current event sink API.
		p.scriptEvents.doWhenKeyPressed(e.Key)
	case *eventStart:
		// eventStart 自己先创建一个 dispatcher Thread。dispatcher 负责读取
		// 已登记的 OnStart sinks，再为每个匹配的 sink 创建独立 Thread。
		// 因而“开始事件分发器”和“各 onStart 脚本”不是同一个协程。
		runStartPhase := func() {
			sinks, ok := p.takeStartSinksFor(e.generation)
			if !ok {
				return
			}
			p.scriptEvents.doWhenStart(sinks, func() bool {
				return p.isBootstrapGenerationCurrent(e.generation)
			})
			p.markStartDispatchedFor(e.generation)
		}
		if gco == nil {
			runStartPhase()
			break
		}
		dispatcher := gco.Create(startEventDispatcher{}, func(coroutine.Thread) int {
			runStartPhase()
			return 0
		})
		if gco.IsInCoroutine() {
			gco.Join(dispatcher)
		}
	case *eventTimer:
		p.scriptEvents.doWhenTimer(e.Time)
	}
}

func (p *Game) fireEvent(ev event) {
	if p.queueEventWithPolicy(ev) {
		return
	}
	if isDebugInstrEnabled() {
		spxlog.Warn("Event buffer is full (policy=%s). Drop event: %v", p.gameRuntimeState.EventQueuePolicy, ev)
	}
}

// Event Dispatch

// doWhenStart 分发一次开始事件快照。
//
// sinks 是当前启动代次已经冻结的 OnStart 订阅；方法先按 Scratch 目标顺序
// 排列，再以 BatchWaitFirstSlice 启动，使每个 handler 都执行到第一次
// Wait/Yield 或结束。shouldRun 在 handler 真正开始前检查该启动代次是否仍有效。
func (p *scriptEventRegistry) doWhenStart(sinks []eventSink, shouldRun func() bool) {
	// 先按 Scratch 目标顺序排列 sinks，再用 BatchWaitFirstSlice 分发：每个
	// OnStart handler 都会成为单独 Thread，并按顺序执行到第一次让出/结束。
	p.dispatchStartSinks(sinksInScratchTargetOrder(activeGame(), sinks), scriptEventDispatch{
		mode:      coroutine.BatchWaitFirstSlice,
		shouldRun: shouldRun,
		run: func(_ coroutine.Thread, ev *eventSink) {
			coreevent.If0(isDebugEventEnabled, func() {
				spxlog.Debug("OnStart: %s", nameOf(ev.Owner))
			})()
			ev.Handler.(func())()
		},
	})
}

// doWhenAwake 同步分发 Awake 事件。
// this 不为 nil 时只匹配该 owner；this 为 nil 时，OnAwake 注册使用的
// MatchOwnerOrNil 会允许全部 owner 参与，相当于全局 Awake。BatchWaitDone
// 保证全部匹配的 Awake handler 永久结束后才返回。
func (p *scriptEventRegistry) doWhenAwake(this threadObj) {
	p.dispatchGlobal(coreevent.BucketAwake, scriptEventDispatch{
		mode:      coroutine.BatchWaitDone,
		matchData: this,
		run: func(_ coroutine.Thread, ev *eventSink) {
			coreevent.If0(isDebugEventEnabled, func() {
				spxlog.Debug("OnAwake: %s", nameOf(ev.Owner))
			})()
			ev.Handler.(func())()
		},
	})
}

// doWhenTimer 异步分发一次计时器事件。
// time 同时用于筛选注册时间近似相等的 sink，并作为参数传给匹配的 handler。
func (p *scriptEventRegistry) doWhenTimer(time float64) {
	p.dispatchGlobal(coreevent.BucketTimer, scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: time,
		run: func(_ coroutine.Thread, ev *eventSink) {
			ev.Handler.(func(float64))(time)
		},
	})
}

// doWhenKeyPressed 异步分发一次按键事件。
//
// 它先分别取得“指定按键”和“任意按键”两个 Bucket 的 Scratch 顺序快照，
// 再按指定按键在前、任意按键在后的顺序合并；key 用于条件筛选和 handler 入参。
func (p *scriptEventRegistry) doWhenKeyPressed(key Key) {
	specific := p.globalSinks(coreevent.BucketKeyPressed)
	anyKey := p.globalSinks(coreevent.BucketAnyKeyPressed)
	p.dispatchSinks(slices.Concat(specific, anyKey), scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: key,
		run: func(_ coroutine.Thread, ev *eventSink) {
			ev.Handler.(func(Key))(key)
		},
	})
}

// doWhenSwipe 异步分发 this 对象上的滑动事件。
// dispatchTarget 先限定 sink.Owner == this，direction 再用于筛选方向并传给 handler。
func (p *scriptEventRegistry) doWhenSwipe(direction Direction, this threadObj) {
	p.dispatchTarget(coreevent.BucketSwipe, this, scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: direction,
		run: func(_ coroutine.Thread, ev *eventSink) {
			ev.Handler.(func(Direction))(direction)
		},
	})
}

// doWhenClick 异步分发 this 对象上的点击事件。
// this 既作为 owner 精确筛选条件，也作为 eventSink.Cond 的匹配数据。
func (p *scriptEventRegistry) doWhenClick(this threadObj) {
	p.dispatchTarget(coreevent.BucketClick, this, scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: this,
		run: func(_ coroutine.Thread, ev *eventSink) {
			coreevent.If0(isDebugEventEnabled, func() {
				spxlog.Debug("OnClick: %s", nameOf(this))
			})()
			ev.Handler.(func())()
		},
	})
}

// doWhenTouchStart 异步分发 this 与 obj 刚开始接触的事件。
// this 决定由哪个目标的 sink 接收事件，obj.sprite 则作为“接触到的另一个精灵”
// 传给用户 handler；二者承担的角色不同。
func (p *scriptEventRegistry) doWhenTouchStart(this threadObj, obj *SpriteImpl) {
	p.dispatchTarget(coreevent.BucketTouchStart, this, scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: this,
		run: func(_ coroutine.Thread, ev *eventSink) {
			coreevent.If0(isDebugEventEnabled, func() {
				spxlog.Debug("OnTouchStart: %s, %s", nameOf(this), obj.name)
			})()
			ev.Handler.(func(Sprite))(obj.sprite)
		},
	})
}

// doWhenCloned 分发刚创建的克隆 this 对应的 OnCloned 事件。
// data 是 CloneWith 携带给用户 handler 的可选数据，不参与 owner 精确筛选。
func (p *scriptEventRegistry) doWhenCloned(this threadObj, data any) {
	// this 是刚创建的克隆对象。dispatchTarget 只匹配 owner == this 的 sinks，
	// 然后 StartBatch 为每个匹配的 OnCloned handler 创建独立 Thread。
	// BatchWaitFirstSlice 让创建者等到这些 Thread 第一次 Wait/Yield 或结束；
	// 它不等待 wait 之后的删除代码执行完，也不会把 OnCloned 放进创建者 Thread。
	p.dispatchTarget(coreevent.BucketCloned, this, scriptEventDispatch{
		mode:      coroutine.BatchWaitFirstSlice,
		matchData: this,
		run: func(_ coroutine.Thread, ev *eventSink) {
			coreevent.If0(isDebugEventEnabled, func() {
				spxlog.Debug("OnCloned: %s", nameOf(this))
			})()
			ev.Handler.(func(any))(data)
		},
	})
}

// doWhenIReceive 分发一条广播消息。
//
// msg 用于筛选消息名并作为 handler 参数，data 是广播附加数据。wait 为 true
// 时采用 BatchWaitDone；否则异步返回。消息 handler 在自己的开始帧内再次进行
// 非等待广播时，before 会让新接收者先等待下一帧，避免递归广播垄断当前帧。
//
// 完整主调用链（编号与函数内部注释对应）：
//
//	doWhenIReceive
//	  -> shouldDeferMessageReceivers                   [1. 判断是否跨帧]
//	  -> dispatchGlobal                               [2. 构造分发]
//	     -> globalSinks -> Manager.Snapshot -> 排序    [3. 冻结接收者]
//	     -> dispatchSinks -> runScriptEventDispatch    [4. 进入 runMu 边界]
//	        -> dispatchScriptEventBatch                [5. 按消息名匹配]
//	        -> scriptEventDispatch.task                [6. 包装 BatchTask]
//	        -> Coroutines.StartBatch                   [7. 创建 Thread]
//	           -> lifecycle/messageEventHandler.start  [8. 登记 active]
//	           -> 打开首个 Latch                       [9. 放行批次]
//	  接收者 goroutine: runThread -> Before            [10. 可选跨帧]
//	                    -> Latch.Wait -> task.Run       [11. 接力执行]
//	                    -> invoke -> run -> 用户 OnMsg  [12. 用户代码]
//	                    -> cleanup                     [13. 清理 active]
//	  BroadcastAndWait: JoinAll                        [14. 等待全部结束]
//	  runThread: finishThread                           [15. 协程收尾]
func (p *scriptEventRegistry) doWhenIReceive(msg string, data any, wait bool) {
	// [消息分发 1] 判断是否处于“消息 handler 刚开始的同一帧又广播”的情况。
	// 该判断不会创建 Thread，只决定下面每个接收者的 Before 是否要跨帧让出。
	deferToNextFrame := p.shouldDeferMessageReceivers(wait)

	// [消息分发 2] 构造本次广播的分发方案，并从 BucketIReceive 取得全局 sink
	// 快照。dispatchGlobal 后续依次完成 Scratch 排序、消息名匹配、BatchTask
	// 构造和 Thread 创建；此处的闭包会被复制进每个匹配接收者的任务中。
	p.dispatchGlobal(coreevent.BucketIReceive, scriptEventDispatch{
		// wait=false 对应普通 Broadcast：只启动任务，不等待 handler 结束。
		// wait=true 对应 BroadcastAndWait：调用者 Join 全部接收者到永久结束。
		mode: eventBatchMode(wait),
		// matchingEventSinks 会把 msg 传给各 sink.Cond。监听指定消息的 sink
		// 只在名称相等时入选；没有 Cond 的 OnMsg__0 会接收任意消息。
		matchData: msg,
		// [消息分发 8] StartBatch 为某个接收者创建并注册 Thread 后，同步调用
		// lifecycle。start 会把新 Thread 记为该消息脚本的 active，停止旧实例，
		// 并返回一个在本次 task.Run 结束时清除 active 的 cleanup。
		lifecycle: func(thread coroutine.Thread, ev *eventSink) func() {
			return ev.Handler.(*messageEventHandler).start(thread)
		},
		// [消息分发 10] Before 在接收者 Thread 已取得 runMu、但尚未通过批次
		// Latch 进入用户 handler 前执行。需要延期时，WaitNextFrame 创建帧等待
		// WaitJob 并 Yield；恢复后仍是同一个 Thread，随后才继续等待批次接力棒。
		before: func(coroutine.Thread) {
			if deferToNextFrame {
				engine.WaitNextFrame()
			}
		},
		// [消息分发 12] 这是匹配接收者最终进入用户消息函数的适配器。
		// event.invoke -> 此 run -> messageEventHandler.run(msg, data)。
		run: func(thread coroutine.Thread, ev *eventSink) {
			// 记录 handler 真正开始用户代码时的帧。若用户代码在本帧再次执行
			// Broadcast，shouldDeferMessageReceivers 会找到这条记录并要求接收者
			// 下一帧再运行；一旦用户 handler 跨帧，帧号比较自然变为不相等。
			p.messageHandlerFrames.Store(thread, itime.Frame())
			// 用户函数无论正常返回还是 panic，都会先删除嵌套广播判定记录；更外层
			// task.Run 的 cleanup 随后再处理 messageEventHandler.active。
			defer p.messageHandlerFrames.Delete(thread)
			// Handler 在注册时保存为 *messageEventHandler，run 字段才是生成代码
			// 传入的真实 OnMsg 函数。到这一行，用户脚本正式开始执行。
			ev.Handler.(*messageEventHandler).run(msg, data)
		},
	})
}

// shouldDeferMessageReceivers 判断本次非等待广播的接收者是否应推迟到下一帧。
//
// 只有当前调用者正是消息 handler Thread，并且仍处于该 handler 开始执行的
// 同一帧时才返回 true。已经 Yield 并跨过帧边界的 handler 已建立自然调度边界，
// wait=true 的广播又必须同步等待，所以这两种情况均不额外延期。
func (p *scriptEventRegistry) shouldDeferMessageReceivers(wait bool) bool {
	// BroadcastAndWait 必须立即建立并等待接收者；没有协程管理器或调用者不是
	// 脚本 Thread 时，也不存在“当前消息脚本在同帧递归广播”的判定上下文。
	if wait || gco == nil || !gco.IsInCoroutine() {
		return false
	}
	// Current 是此刻持有 runMu 的 Thread，也就是正在调用 Broadcast 的脚本。
	thread := gco.Current()
	if thread == nil {
		return false
	}
	// 只有该 Thread 已经进入某个消息 handler，且当前帧仍等于它开始用户代码
	// 时记录的帧，才需要在新接收者的 Before 中插入 WaitNextFrame。
	handlerFrame, ok := p.messageHandlerFrames.Load(thread)
	return ok && handlerFrame == itime.Frame()
}

// doWhenBackdropChanged 分发背景切换完成事件。
// name 用于筛选监听特定背景的 sink，并传给 handler；wait 决定异步返回还是
// 等待全部匹配 handler 永久结束。
func (p *scriptEventRegistry) doWhenBackdropChanged(name BackdropName, wait bool) {
	p.dispatchGlobal(coreevent.BucketBackdropChanged, scriptEventDispatch{
		mode:      eventBatchMode(wait),
		matchData: name,
		run: func(_ coroutine.Thread, ev *eventSink) {
			ev.Handler.(func(BackdropName))(name)
		},
	})
}
