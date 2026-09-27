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

// scriptEventBindings 是挂在 Game 或 SpriteImpl 上的事件注册入口。
// 它不直接保存每一种事件的处理列表，而是保存当前对象的 owner，
// 再把注册动作转交给同一个 scriptEventRegistry。这样 Game、每个精灵
// 都可以使用相同的 OnKey/OnClick/OnMsg 等 API，同时事件处理器仍能
// 通过 owner 区分“由谁注册”和“应该派发给谁”。
type scriptEventBindings struct {
	// 同一局 Game 的事件注册表。Game 的 scriptEvents 持有唯一实例，
	// SpriteImpl 的 bindings 通过该指针共享注册表，而不是各自创建一套。
	*scriptEventRegistry
	// 当前注册者，可以是 *Game 或 *SpriteImpl；事件派发时用于筛选和排序。
	owner threadObj
}

// scriptEventRegistry 是一局游戏的 Go 脚本事件中心。
//
// 它负责三件事：
//  1. 把 OnStart、OnKey、OnClick、OnCond、OnMsg 等注册为 event.Sink；
//  2. 按事件类型、owner 和 Cond 快照匹配处理器；
//  3. 事件命中后构造 coroutine.Task，交给 gco 创建并调度处理器协程。
//
// 它不是底层输入队列：键盘/鼠标/物理回调的 pending、ready 缓存分别维护在
// internal/engine 中；这些底层输入被转换成高层事件后，才会调用本注册表。
// 它也不等同于 Game.events 通道，Broadcast、OnCond、OnTouchStart 等事件
// 可以绕过 Game.events，直接进入这里的匹配和派发流程。
type scriptEventRegistry struct {
	// 所属游戏，用于获取当前精灵顺序、舞台 owner 和派发上下文。
	game *Game
	// 按 Bucket 保存所有事件注册项。每一项 Sink 包含 Owner、匹配函数和 Handler。
	manager coreevent.Manager
	// 记录正在执行的消息处理器，用于 Broadcast 递归/级联派发时控制同一接收者
	// 在当前帧和脚本轮次中的执行顺序。该字段只在持有调度器 runMu 的脚本切片中访问。
	messageExecutions map[coroutine.Thread]messageReceiverExecution
	// StopAll 的代际计数。OnStart 批量启动期间若发生全局停止，新启动的处理器
	// 会通过代际变化在首次让出时被取消。
	stopAllEpoch atomic.Uint64
	// 正在登记、尚未真正进入 Run 的 OnStart 协程集合；用于启动阶段的生命周期保护。
	pendingStartThreads sync.Map // map[coroutine.Thread]struct{}
	// pendingConditions 保存 OnEngineBeforeUpdate 已完成求值、等待
	// OnEngineUpdate 派发的条件处理器快照，仅由引擎帧线程访问。
	pendingConditions []eventSink
}

// messageDispatchContext 记录一次 Broadcast 级联过程中已经获得执行机会的接收者。
// 同一接收者在同一帧、同一脚本轮次中重复收到消息时，会先让出到下一轮，
// 避免递归广播无限重入。其状态只在持有调度器 runMu 的脚本切片中访问。
type messageDispatchContext struct {
	frame     int64
	round     uint64
	receivers map[*messageEventHandler]struct{}
}

// messageReceiverExecution 把当前消息处理协程和本次 Broadcast 的上下文关联起来，
// 使处理器内部再次 Broadcast 时仍能沿用同一套接收者去重/轮次规则。
type messageReceiverExecution struct {
	context  *messageDispatchContext
	receiver *messageEventHandler
}

// startEventDispatcher 是 OnStart 派发阶段的协程 owner 标记，用来把启动事件
// 的登记和处理从普通事件循环中区分出来。
type startEventDispatcher struct{}

// Click Dispatch
type clicker interface {
	threadObj
	doWhenClick(this threadObj)
	getProxy() *engine.Sprite
	Visible() bool
}

// OnStart 登记项目级一次性启动处理器；登记本身不会创建或调度协程。
//
// 直接调用方：Game/生成精灵的 Main，包括 cloneSprite 对克隆 Main 的重跑；总体流程
// 调用方：bootstrap 完成后的 dispatchStartEventIfNeeded。若全局启动快照尚未生成，
// Sink 会进入 BucketStart，稍后统一创建协程；若快照已经生成，运行期克隆的迟到注册
// 会被静默忽略，因为克隆自己的出生生命周期应由 OnCloned 表达，而非重放 OnStart。
func (p *scriptEventBindings) OnStart(onStart func()) {
	sink := coreevent.NewSink(p.owner, onStart)
	if p.scriptEventRegistry.manager.TryAddStart(sink) {
		return
	}
	if sprite, ok := sink.Owner.(*SpriteImpl); ok && sprite.spriteState.Cloned {
		return
	}
	spxlog.Warn("Event: ignoring late OnStart registration for %s", nameOf(sink.Owner))
}

func (p *scriptEventBindings) OnClick(onClick func()) {
	owner := p.owner
	p.scriptEventRegistry.manager.Add(coreevent.BucketClick, newScriptEventSink(owner, onClick, coroutine.RestartExisting, coreevent.MatchOwner(owner)))
}

func (p *scriptEventBindings) OnAnyKey(onKey func(key Key)) {
	p.registerKeyHandler([]Key{KeyAny}, onKey)
}

func (p *scriptEventBindings) OnTimer(time float64, call func()) {
	itime.RegisterTimer(time)
	p.scriptEventRegistry.manager.Add(coreevent.BucketTimer, coreevent.NewSink(
		p.owner,
		func(float64) {
			if isDebugEventEnabled() {
				spxlog.Debug("OnTimer: %s", nameOf(p.owner))
			}
			if call != nil {
				call()
			}
		},
		coreevent.MatchApproxFloat(time, 0.001),
	))
}

func (p *scriptEventBindings) OnKey__0(key Key, onKey func()) {
	handler := func(Key) {
		if isDebugEventEnabled() {
			spxlog.Debug("OnKey: %v, %s", key, nameOf(p.owner))
		}
		if onKey != nil {
			onKey()
		}
	}
	p.registerKeyHandler([]Key{key}, handler)
}

func (p *scriptEventBindings) OnSwipe__0(direction Direction, onSwipe func()) {
	p.scriptEventRegistry.manager.Add(coreevent.BucketSwipe, coreevent.NewSink(
		p.owner,
		func(Direction) {
			if isDebugEventEnabled() {
				spxlog.Debug("OnSwipe: %v, %s", direction, nameOf(p.owner))
			}
			if onSwipe != nil {
				onSwipe()
			}
		},
		coreevent.MatchValue(direction),
	))
}

func (p *scriptEventBindings) OnKey__1(keys []Key, onKey func(Key)) {
	handler := func(key Key) {
		if isDebugEventEnabled() {
			spxlog.Debug("OnKey: %v, %s", keys, nameOf(p.owner))
		}
		if onKey != nil {
			onKey(key)
		}
	}
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
		func(msg string, _ any) {
			if isDebugEventEnabled() {
				spxlog.Debug("OnMsg: %s, %s", msg, nameOf(p.owner))
			}
			if onMsg != nil {
				onMsg()
			}
		},
		coreevent.MatchValue(msg),
	)
}

func (p *scriptEventBindings) OnBackdrop__0(onBackdrop func(name BackdropName)) {
	p.scriptEventRegistry.manager.Add(coreevent.BucketBackdropChanged, newScriptEventSink(p.owner, onBackdrop, coroutine.RestartExisting))
}

func (p *scriptEventBindings) OnBackdrop__1(name BackdropName, onBackdrop func()) {
	handler := func(name BackdropName) {
		if isDebugEventEnabled() {
			spxlog.Debug("OnBackdrop: %s, %s", name, nameOf(p.owner))
		}
		if onBackdrop != nil {
			onBackdrop()
		}
	}
	p.scriptEventRegistry.manager.Add(coreevent.BucketBackdropChanged, newScriptEventSink(
		p.owner, handler, coroutine.RestartExisting, coreevent.MatchValue(name),
	))
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

// bindScriptEvents 初始化本局 Game 的事件绑定，使 Game 自身也能注册
// OnStart、OnKey、OnMsg 等脚本事件。所有 SpriteImpl 随后共享 p.scriptEvents。
func (p *Game) bindScriptEvents() {
	p.scriptEvents.game = p
	p.scriptEventBindings.bind(&p.scriptEvents, p)
}

// bind 将事件注册入口绑定到指定注册表和 owner。
// owner 决定事件属于 Game 还是某个 SpriteImpl，也是后续匹配、排序和清理的依据。
func (p *scriptEventBindings) bind(registry *scriptEventRegistry, owner threadObj) {
	p.scriptEventRegistry = registry
	p.owner = owner
}

// clearHandlers 删除当前 owner 注册的全部 Sink。
// 精灵销毁、克隆状态清理或脚本重载时使用，避免旧对象继续收到事件。
func (p *scriptEventBindings) clearHandlers() {
	p.scriptEventRegistry.manager.DeleteOwner(p.owner)
}

func (p *scriptEventBindings) doWhenSwipe(direction Direction, target threadObj) {
	p.scriptEventRegistry.doWhenSwipe(direction, target)
}

func (p *scriptEventBindings) onAwake(onAwake func()) {
	owner := p.owner
	p.scriptEventRegistry.manager.Add(coreevent.BucketAwake, coreevent.NewSink(owner, onAwake, coreevent.MatchOwnerOrNil(owner)))
}

// registerKeyHandler 将一个按键处理器登记到具体按键桶或任意按键桶。
// 使用 IgnoreWhileRunning 策略：同一处理器仍在执行时，新的同类按键不会重入。
func (p *scriptEventBindings) registerKeyHandler(keys []Key, handler func(Key)) {
	if len(keys) == 0 {
		return
	}
	sink := newScriptEventSink(p.owner, handler, coroutine.IgnoreWhileRunning)
	if slices.Contains(keys, KeyAny) {
		p.scriptEventRegistry.manager.Add(coreevent.BucketAnyKeyPressed, sink)
		return
	}
	sink.Cond = coreevent.MatchAnyOf(slices.Clone(keys))
	p.scriptEventRegistry.manager.Add(coreevent.BucketKeyPressed, sink)
}

// registerMessageHandler 登记 IReceive/Broadcast 处理器。
// 消息处理器采用 RestartExisting：同一 owner 再次收到消息时取消旧执行并启动新执行，
// 可选 cond 用于筛选具体消息名或其他消息条件。
func (p *scriptEventBindings) registerMessageHandler(handler func(string, any), cond ...func(any) bool) {
	p.scriptEventRegistry.manager.Add(coreevent.BucketIReceive, newScriptEventSink(
		p.owner, handler, coroutine.RestartExisting, cond...,
	))
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

	return engine.Managers().SpriteMgr.CheckCollisionWithPoint(syncSprite.GetId(), point, true)
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
		runStartPhase := func() {
			sinks, ok := p.takeStartSinks(e.generation)
			if !ok {
				return
			}
			p.scriptEvents.doWhenStart(sinks, func() bool {
				return p.isCurrentBootstrap(e.generation)
			})
			p.markStartDispatched(e.generation)
		}
		if gco == nil {
			runStartPhase()
			break
		}
		dispatcher := gco.Create(startEventDispatcher{}, func(coroutine.Thread) {
			runStartPhase()
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
		spxlog.Warn("Event dropped (policy=%s): %v", p.eventQueueState.EventQueuePolicy, ev)
	}
}

// Event Dispatch doWhenStart在第一层协程中运行
func (p *scriptEventRegistry) doWhenStart(sinks []eventSink, shouldRun func() bool) {
	p.dispatchStartSinks(sinksInScratchTargetOrder(p.game, sinks), scriptEventDispatch{
		mode: coroutine.BatchWaitFirstSlice,
		run: func(_ coroutine.Thread, ev *eventSink) {
			if shouldRun != nil && !shouldRun() {
				return
			}
			if isDebugEventEnabled() {
				spxlog.Debug("OnStart: %s", nameOf(ev.Owner))
			}
			ev.Handler.(func())()
		},
	})
}

func (p *scriptEventRegistry) doWhenAwake(this threadObj) {
	p.dispatchGlobal(coreevent.BucketAwake, scriptEventDispatch{
		mode:      coroutine.BatchWaitDone,
		matchData: this,
		run: func(_ coroutine.Thread, ev *eventSink) {
			if isDebugEventEnabled() {
				spxlog.Debug("OnAwake: %s", nameOf(ev.Owner))
			}
			ev.Handler.(func())()
		},
	})
}

func (p *scriptEventRegistry) doWhenTimer(time float64) {
	p.dispatchGlobal(coreevent.BucketTimer, scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: time,
		run: func(_ coroutine.Thread, ev *eventSink) {
			ev.Handler.(func(float64))(time)
		},
	})
}

func (p *scriptEventRegistry) doWhenKeyPressed(key Key) {
	specific := p.globalSinks(coreevent.BucketKeyPressed)
	anyKey := p.globalSinks(coreevent.BucketAnyKeyPressed)
	p.dispatchSinks(slices.Concat(specific, anyKey), scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: key,
		run: func(_ coroutine.Thread, ev *eventSink) {
			ev.Handler.(*scriptEventHandler[func(Key)]).run(key)
		},
	})
}

func (p *scriptEventRegistry) doWhenSwipe(direction Direction, this threadObj) {
	p.dispatchTarget(coreevent.BucketSwipe, this, scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: direction,
		run: func(_ coroutine.Thread, ev *eventSink) {
			ev.Handler.(func(Direction))(direction)
		},
	})
}

func (p *scriptEventRegistry) doWhenClick(this threadObj) {
	p.dispatchTarget(coreevent.BucketClick, this, scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: this,
		run: func(_ coroutine.Thread, ev *eventSink) {
			if isDebugEventEnabled() {
				spxlog.Debug("OnClick: %s", nameOf(this))
			}
			ev.Handler.(*scriptEventHandler[func()]).run()
		},
	})
}

func (p *scriptEventRegistry) doWhenTouchStart(this threadObj, obj *SpriteImpl) {
	p.dispatchTarget(coreevent.BucketTouchStart, this, scriptEventDispatch{
		mode:      coroutine.BatchAsync,
		matchData: this,
		run: func(_ coroutine.Thread, ev *eventSink) {
			if isDebugEventEnabled() {
				spxlog.Debug("OnTouchStart: %s, %s", nameOf(this), obj.name)
			}
			ev.Handler.(func(Sprite))(obj.sprite)
		},
	})
}

// doWhenCloned 将克隆生命周期事件直接派发给新精灵自己的注册项。
//
// 直接调用方：SpriteImpl.doWhenCloned（由嵌入的 scriptEventBindings 提升）最终来自
// dispatchCloneLifecycle；总体流程调用方：精灵脚本的 Clone API。这里不进入普通事件
// 队列，而是立即 snapshot BucketCloned，并只选择 Owner == this 的 Sink。
// BatchWaitFirstSlice 表示克隆调用方等待处理器获得一次执行机会，而不是等待整个
// OnCloned 完成；处理器若调用 Wait/WaitNextFrame，克隆流程会在该挂起点之后继续。
func (p *scriptEventRegistry) doWhenCloned(this threadObj, data any) {
	p.dispatchTarget(coreevent.BucketCloned, this, scriptEventDispatch{
		mode:      coroutine.BatchWaitFirstSlice,
		matchData: this,
		run: func(_ coroutine.Thread, ev *eventSink) {
			if isDebugEventEnabled() {
				spxlog.Debug("OnCloned: %s", nameOf(this))
			}
			ev.Handler.(func(any))(data)
		},
	})
}

func (p *scriptEventRegistry) doWhenIReceive(msg string, data any, wait bool) {
	context := p.currentMessageDispatchContext()
	p.dispatchGlobal(coreevent.BucketIReceive, scriptEventDispatch{
		mode:      eventBatchMode(wait),
		matchData: msg,
		run: func(thread coroutine.Thread, ev *eventSink) {
			receiver := ev.Handler.(*messageEventHandler)
			if thread != nil {
				context.waitForTurn(thread, receiver)
				if p.messageExecutions == nil {
					p.messageExecutions = make(map[coroutine.Thread]messageReceiverExecution)
				}
				p.messageExecutions[thread] = messageReceiverExecution{
					context:  context,
					receiver: receiver,
				}
				// Yield reacquires runMu before cancellation unwinds this defer;
				// runThread releases it only after Run and its defers finish.
				defer delete(p.messageExecutions, thread)
			}
			receiver.run(msg, data)
		},
	})
}

func (p *scriptEventRegistry) currentMessageDispatchContext() *messageDispatchContext {
	if gco == nil || !gco.IsInCoroutine() {
		return new(messageDispatchContext)
	}
	execution, ok := p.messageExecutions[gco.Current()]
	if !ok {
		return new(messageDispatchContext)
	}
	execution.context.claimTurn(execution.receiver)
	return execution.context
}

func (p *messageDispatchContext) waitForTurn(thread coroutine.Thread, receiver *messageEventHandler) {
	for !p.claimTurn(receiver) {
		gco.YieldToNextRoundFor(thread)
	}
}

func (p *messageDispatchContext) claimTurn(receiver *messageEventHandler) bool {
	frame, round := itime.Frame(), gco.ScriptRound()
	if p.frame != frame || p.round != round {
		p.frame, p.round = frame, round
		clear(p.receivers)
	}
	if p.receivers == nil {
		p.receivers = make(map[*messageEventHandler]struct{})
	}
	if _, claimed := p.receivers[receiver]; claimed {
		return false
	}
	p.receivers[receiver] = struct{}{}
	return true
}

func (p *scriptEventRegistry) doWhenBackdropChanged(name BackdropName, wait bool) {
	p.dispatchGlobal(coreevent.BucketBackdropChanged, scriptEventDispatch{
		mode:      eventBatchMode(wait),
		matchData: name,
		run: func(_ coroutine.Thread, ev *eventSink) {
			ev.Handler.(*scriptEventHandler[func(BackdropName)]).run(name)
		},
	})
}
