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
	"sync/atomic"

	"github.com/goplus/spbase/mathf"
	coreruntime "github.com/goplus/spx/v3/internal/core/runtime"
	"github.com/goplus/spx/v3/internal/engine"
	spxlog "github.com/goplus/spx/v3/internal/log"
)

// 本文件是 Go 运行时与 Godot 场景代理之间的同步边界。
//
// Go 侧保存 Sprite 的逻辑状态（位置、旋转、缩放、服装、可见性和生命周期），
// Godot 侧保存真正参与渲染、物理和像素查询的节点。两侧不能在每个属性变更点
// 都直接互相调用，因此本文件把修改聚合到帧边界：
//   - OnEngineUpdate 阶段收集 Go -> Godot 的批量变换、可见性和删除；
//   - 同一阶段随后从 Godot/物理引擎批量回读启用物理精灵的位置；
//   - 同一帧的 OnEngineRender 准备阶段补交协程恢复后的变化，并派发已封存的触发事件。
//
// OnEngineRender 虽名为 Render，但不是 Godot 的独立渲染回调；它和 OnEngineUpdate
// 都由 internal/engine.onUpdate 在一次 Godot 帧更新中依次调用，中间由 gco.Update
// 恢复到期的 Go 协程。这里的“Render”表示真正绘制/截图前的状态准备阶段。
//
// SpriteSyncBuffer 的数据最终进入 Godot 的 BatchUpdateTransforms ABI。该 ABI 使用
// float32 数组传输旧版对象 ID 和 8 个变换/显示字段，所以这里必须统一处理角度单位、
// 渲染偏移、可见性以及“同一逻辑版本只发送一次”的去重规则。直接操作 Godot 节点的
// 方法则必须在 Godot 主线程执行；需要跨线程或从 Go 协程进入引擎时统一使用
// engine.WaitMainThread。

// -----------------------------------------------------------------------------
// 精灵代理生命周期
// -----------------------------------------------------------------------------
const (
	// cloneProxyPublished 表示克隆代理已经完成初始化，可以正常参与渲染和批量同步。
	cloneProxyPublished uint32 = iota
	// cloneProxyPending 表示克隆代理已经创建，但 OnCloned/Main 的首段初始化尚未完成。
	// Godot 节点在该状态下必须保持隐藏，避免渲染到半初始化的克隆。
	cloneProxyPending
	// cloneProxyReady 表示克隆初始化已完成，等待下一次批量收集时公开代理。
	// collectProxyUpdate 会在公开前先应用服装和图层，再发送可见性。
	cloneProxyReady
)

// cloneProxyPublication 记录运行时克隆从“隐藏初始化”到“可公开”的状态。
//
// 克隆流程会先创建 Godot 节点，再执行用户的 OnCloned/Main 初始化代码。若立即
// 显示节点，渲染线程可能看到尚未设置好的服装、图层或变换；因此状态机会把公开
// 延迟到下一次代理批量同步。碰撞/触摸查询又可能在此期间同步访问代理，所以另有
// 一个临时的 sensing 可见性租约，只允许查询路径暂时打开可见性，不改变渲染公开时机。
type cloneProxyPublication struct {
	// state 是上面的三态状态机。状态主要由引擎主线程推进，读取使用原子操作，
	// 以覆盖查询桥接路径可能发生的重入读取。
	state uint32
	// sensingVisibilityLeases 是查询期间的临时可见性租约计数。
	// 使用计数而不是 bool，是因为嵌套的 touching/碰撞查询必须在最内层退出后仍保持
	// 可见；它通常由主线程维护，但 effectiveProxyVisibility 可能从通用同步路径读取，
	// 因而使用原子类型避免读取到不一致的状态。
	sensingVisibilityLeases atomic.Int32
}

// dispatchStartEventIfNeeded 在 bootstrap 完成后派发一次项目启动事件。
//
// 直接调用方：Game.runFrameScripts；顶层入口：Godot 每帧 update 回调经
// Game.OnEngineUpdate 进入的脚本帧循环。scheduleStartEvent 只负责生成事件，
// 真正的处理器登记/启动由 handleEvent 完成；因此这里不会重复派发，也不会绕过
// Go 侧事件调度器直接调用用户函数。
func (p *Game) dispatchStartEventIfNeeded() {
	if ev := p.scheduleStartEvent(); ev != nil {
		p.handleEvent(ev)
	}
}

// updateSpriteProxies 在 GameUpdate 阶段、Go 协程统一恢复前执行第一轮代理同步。
//
// 处理顺序如下：
//  1. 更新相机状态；
//  2. 取得当前活跃的 Shape，并推进 Monitor 等非精灵 Shape 的逐帧逻辑；
//  3. 收集 SpriteImpl 的脏代理数据，同时处理待销毁精灵；
//  4. 通过一次批量 FFI 调用，把变换、可见性和删除信息发送给 Godot。
//
// 这个函数只负责 Go -> Godot 的同步，不负责从物理引擎读取位置；位置回读由
// 后面的 pullPhysicsPositions 完成。runFrameScripts 在此之前只登记启动/逐帧回调，
// 回调协程在稍后的 gco.Update 才真正恢复；它们产生的变化由 OnEngineRender 中的
// syncPostCoroutineVisuals 再补交一次。
//
// 直接调用方：Game.OnEngineUpdate；顶层入口：Godot 帧更新经 native/Web FFI 进入
// internal/engine.onUpdate 的 GameUpdate 阶段。
func (p *Game) updateSpriteProxies() {
	p.camera.onUpdate()
	activeShapes := p.shapeMgr.getTempShapes()
	p.shapeMgr.flushActivate(activeShapes)
	// 气泡布局依赖协程恢复后可能发生的视觉变化，因此统一放到
	// syncPostCoroutineVisuals；本阶段只推进非气泡 Shape 的激活逻辑。
	p.flushSpriteProxyChanges(activeShapes)
}

// syncPostCoroutineVisuals 在协程恢复后、Godot 实际绘制前补交视觉变化。
//
// 直接调用方：Game.OnEngineRender；顶层入口：Godot 帧更新经 native/Web FFI 进入
// internal/engine.onUpdate，并在 gco.Update 之后进入 GameRender 准备阶段。
// gco.Update 可能在 OnEngineUpdate 的代理批处理之后继续修改服装、变换或可见性，
// 所以这里再次收集代理，但不重复推进脚本帧、物理激活或逻辑时间。气泡布局也在
// 此处执行，确保它读取的是本帧最终的精灵边界；截图提交发生在本阶段返回之后。
func (p *Game) syncPostCoroutineVisuals() {
	p.camera.onUpdate()

	activeShapes := p.shapeMgr.getTempShapes()

	p.flushSpriteProxyChanges(activeShapes)

	// 视觉代理先提交，再依据最终边界重新布局文本气泡。
	p.shapeMgr.flushBubbleVisuals(activeShapes)
}

// flushSpriteProxyChanges 收集一个稳定的 Shape 快照，并完成本轮代理同步。
//
// 直接调用方：updateSpriteProxies、syncPostCoroutineVisuals；顶层调用方分别是
// Game.OnEngineUpdate 和 Game.OnEngineRender。activeShapes 是 collectProxyUpdates
// 使用的稳定快照；待销毁对象来自独立的 shapeManager.destroyItems 队列。
// Go 侧按“清空可复用 buffer -> 收集更新 -> 追加删除 ID -> 一次 FFI”组织数据；
// Godot 侧会先完整校验数据包，再先执行删除、后执行更新，因此同包删除具有优先级。
// 批处理完成后才清除相机 dirty 标志。syncBuffer 是非并发复用的暂存区。
func (p *Game) flushSpriteProxyChanges(activeShapes []Shape) {
	p.syncBuffer.Clear()
	p.shapeMgr.collectProxyUpdates(activeShapes, p.syncBuffer)
	p.shapeMgr.flushDestroy(p.syncBuffer)
	p.flushSyncBuffer()
	p.camera.setDirtyFlag(false)
}

// flushSyncBuffer 在存在更新或删除时，把 SpriteSyncBuffer 编码后发送给 Godot。
//
// SpriteSyncBuffer.Serialize 的布局是：
//
//	[updateCount, deleteCount, update_0..., update_n..., deleteID_0..., deleteID_n...]
//
// 每条 update 有 9 个 float32：对象 ID、x、y、弧度制旋转、scaleX、scaleY、
// renderOffsetX、renderOffsetY、visible。这里通过 FlushSerializedBuffer 统一保证
// 空批次不进入 FFI。engine.SyncBatchUpdateSprites 最终调用 SpriteMgr 的 native/Web
// ABI；Godot 侧要求该批处理在引擎主线程执行，并会校验长度、有限数值和对象 ID。
//
// 此协议中的 ID 是“数值型 float32”，不是按位拆分编码；Serialize 会要求 int64 ID
// 非负且能经 float32 精确往返，否则在进入 FFI 前 panic。序列化结果引用 syncBuffer
// 的复用内存，只能由当前同步调用即时消费，不能跨下一次 buffer 修改持有或并发使用。
// 直接调用方：flushSpriteProxyChanges（另有单元测试）；顶层入口：OnEngineUpdate 和
// OnEngineRender 两轮代理同步。更新数和删除数都为 0 时不会序列化，也不会调用 FFI。
func (p *Game) flushSyncBuffer() {
	coreruntime.FlushSerializedBuffer(
		p.syncBuffer.UpdateCount(),
		p.syncBuffer.DeleteCount(),
		p.syncBuffer.Serialize,
		engine.SyncBatchUpdateSprites,
	)
}

// pullPhysicsPositions 把 Godot/物理引擎中的精灵位置批量同步回 Go 侧。
//
// 它只选择已经创建 SyncSprite 代理且启用了物理的 SpriteImpl，然后使用代理的
// Id 组成一次查询请求。底层 BatchRetrievePositions 返回按 [x, y] 排列的位置数组，
// 再由 applyPhysicsPosition 写回 SpriteImpl 的变换状态。没有候选精灵或查询失败时，
// 本帧不会写回位置；某一项坐标为 NaN 时跳过该项，返回数组长度不足时停止处理缺失项。
// 这里的请求 ID 保持 int64/GdObj 数组，不使用上面的 float32 变换数据包；返回位置已
// 从 Godot 坐标系转换为 SPX 坐标系。输出切片也是可复用暂存区，下次查询会覆盖内容。
//
// 因此，updateSpriteProxies 是 Go -> Godot，而本函数是 Godot/物理 -> Go；两者
// 在 OnEngineUpdate 中连续执行，形成一轮双向同步。
//
// 直接调用方：Game.OnEngineUpdate；顶层入口：Godot 的逐帧 update 回调。
func (p *Game) pullPhysicsPositions() {
	coreruntime.SyncBatchPositions(
		p.getTempShapes(),
		func(item Shape) bool {
			sprite, ok := item.(*SpriteImpl)
			return ok && sprite.shouldPullPhysicsPosition()
		},
		func(item Shape) int64 {
			return int64(item.(*SpriteImpl).runtimeState.SyncSprite.Id)
		},
		p.syncBuffer.GetPositions,
		func(item Shape, x, y float64) {
			item.(*SpriteImpl).applyPhysicsPosition(x, y)
		},
	)
}

// processPhysicsTriggers 消费物理引擎在帧边界缓存到 ready 的触发事件。
//
// 物理层回调只记录 Src/Dst，不在回调现场运行用户脚本；onUpdate() 开始时再将
// pending 切换为 ready，保证本帧消费的是稳定快照。这里通过 GetTriggerEvents
// 取出快照，过滤类型不符、逻辑隐藏或已进入 IsDying 状态的对象，然后调用
// fireTouchStart。destroy 的常规流程会先隐藏并标记对象，所以迟到事件也会被排除。
// fireTouchStart 会进入事件系统，以 BatchAsync 方式登记/启动对应的 OnTouchStart
// 协程；本函数本身不执行用户处理器主体。
//
// 直接调用方：Game.OnEngineRender；顶层入口：internal/engine.onUpdate 在 gco.Update
// 之后执行的 GameRender 准备阶段；
// 事件来源：Godot 物理触发信号 -> engine.Sprite.OnTriggerEnter -> pending/ready 队列。
func (p *Game) processPhysicsTriggers() {
	p.triggerEvents = engine.GetTriggerEvents(p.triggerEvents[:0])
	coreruntime.ProcessTriggerPairs(
		p.triggerEvents,
		func(target any) (*SpriteImpl, bool) {
			sprite, ok := target.(*SpriteImpl)
			return sprite, ok
		},
		isSpriteTouchable,
		func(srcSprite, dstSprite *SpriteImpl) {
			srcSprite.spriteState.HasOnTouchStart = true
			srcSprite.fireTouchStart(dstSprite)
		},
		func() {
			spxlog.Info("Physics error: unexpected trigger pair - invalid sprite types")
		},
	)
	clear(p.triggerEvents)
	p.triggerEvents = p.triggerEvents[:0]
}

// -----------------------------------------------------------------------------
// 基础对象的服装与图层同步
// -----------------------------------------------------------------------------

// scheduleCostumeUpdate 把待处理的服装更新切换到 Godot 主线程执行。
//
// 直接调用方：Game.setupBackdrop；顶层入口：舞台背景加载/切换后的布局流程。
// Godot 的场景节点和渲染资源通常只能在引擎主线程安全修改，因此即使调用者来自
// Go 协程，也必须通过 engine.WaitMainThread 串行进入 applyCostumeUpdate。
func (p *baseObj) scheduleCostumeUpdate() {
	engine.WaitMainThread(func() {
		p.applyCostumeUpdate()
	})
}

// applyCostumeUpdate 将 Go 侧暂存的图层和服装变化提交给 Godot 精灵或舞台代理。
//
// 它主要由 collectProxyUpdate 调用，而 collectProxyUpdate 在一帧中有两个入口：
//  1. OnEngineUpdate -> updateSpriteProxies：提交进入 gco.Update 前已经产生的变化；
//  2. OnEngineRender -> syncPostCoroutineVisuals：提交 gco.Update 恢复脚本后新产生的变化。
//
// 初始化/重建代理、碰撞查询同步以及播放动画前也会直接调用本函数，确保这些操作看到
// 最新服装。普通隐藏精灵会跳过帧同步入口，脏标记一直保留到重新显示或查询同步。
//
// 图层、纹理、纹理图集、渲染缩放和图集 UV 不会写入 SpriteSyncBuffer；本函数会逐项
// 调用 SyncSprite 接口直接修改 Godot 节点。调用前必须保证 SyncSprite 已经创建。
//
// 直接调用方：scheduleCostumeUpdate、rebuildRuntimeProxy、collectProxyUpdate、
// ensureProxyQueryStateSynced 和 animationComponent.doAnimation；顶层调用方覆盖背景布局、
// 精灵初始化、逐帧同步、碰撞/点击查询及动画播放。
func (p *baseObj) applyCostumeUpdate() {
	syncSprite := p.runtimeState.SyncSprite
	// 图层变化与服装变化使用独立的脏标记。未启用 Godot 图层排序模式时，
	// 这里直接更新节点 ZIndex；无论采用哪种排序方式，本轮都会消费图层脏标记。
	if p.runtimeState.IsLayerDirty {
		if !engine.HasLayerSortMethod() {
			syncSprite.SetZIndex(int64(p.runtimeState.Layer))
		}
		p.runtimeState.IsLayerDirty = false
	}
	// 没有服装变化时只需处理上面的图层变化。
	if !p.runtimeState.IsCostumeDirty {
		return
	}
	p.runtimeState.IsCostumeDirty = false
	path := p.getCostumePath()
	renderScale := p.getCostumeRenderScale()
	// 图集服装除了更新纹理区域和渲染缩放，还要同步 shader 使用的 UV 重映射参数。
	if p.isCostumeAtlas() {
		rect := p.getCostumeAtlasRegion()
		syncSprite.UpdateTextureAtlas(path, rect, renderScale, !p.runtimeState.IsAnimating)
		p.applyAtlasUVRemap()
		return
	}
	// 普通服装直接更新纹理和渲染缩放。动画播放期间保留动画当前控制的纹理帧。
	syncSprite.UpdateTexture(path, renderScale, !p.runtimeState.IsAnimating)
}

// applyAtlasUVRemap 把当前图集服装的 UV 矩形写入 Godot 材质参数。
//
// 直接调用方：applyCostumeUpdate；顶层入口与服装同步一致。Godot shader 中的
// atlas_uv_rect2 使用 Vec4(position.x, position.y, size.x, size.y)，用于把精灵局部
// UV 重映射到整张图集对应区域；缺少这一步会导致材质效果按整张图集采样。
func (p *baseObj) applyAtlasUVRemap() {
	uvRemap := p.getCostumeAtlasUvRemap()
	val := mathf.NewVec4(uvRemap.Position.X, uvRemap.Position.Y, uvRemap.Size.X, uvRemap.Size.Y)
	p.setMaterialParamsVec4("atlas_uv_rect2", val, true)
}

// beginCloneProxyPublication 为运行时克隆建立“延迟公开”状态机。
//
// 直接调用方：cloneSprite；顶层入口：Clone/克隆创建流程。只有运行时克隆使用该
// 门控，项目配置中静态加载的精灵不会进入 Pending。调用发生在 initRuntimeProxy
// 之前，使新建 Godot 代理从第一刻起就保持隐藏。
func (p *SpriteImpl) beginCloneProxyPublication() {
	publication := &cloneProxyPublication{}
	atomic.StoreUint32(&publication.state, cloneProxyPending)
	p.proxyPublication = publication
}

// cloneProxyPublicationState 原子读取克隆代理当前的公开状态。
//
// 直接调用方：isCloneProxyPublicationBlocked、isCloneProxyPublicationReady 和
// collectProxyUpdate；顶层用于碰撞查询门控、测试状态观测及帧末代理公开。
// 非克隆精灵没有 publication，按“已经公开”处理，以免普通代理进入额外分支。
func (p *SpriteImpl) cloneProxyPublicationState() uint32 {
	if p.proxyPublication == nil {
		return cloneProxyPublished
	}
	return atomic.LoadUint32(&p.proxyPublication.state)
}

// isCloneProxyPublicationBlocked 判断代理是否仍处于 Pending/Ready、尚未正式公开。
//
// 直接调用方：SpriteImpl.touchingSprite；顶层入口：Touching 等精灵碰撞查询。
// 被门控的克隆要走主线程临时可见性租约，不能直接采用普通查询路径。
func (p *SpriteImpl) isCloneProxyPublicationBlocked() bool {
	return p.cloneProxyPublicationState() != cloneProxyPublished
}

// isCloneProxyPublicationReady 判断克隆初始化是否完成且正在等待下一次批量公开。
// 当前主要用于生命周期测试和状态断言；生产公开动作由 collectProxyUpdate 直接读取
// cloneProxyPublicationState 后执行。
func (p *SpriteImpl) isCloneProxyPublicationReady() bool {
	return p.cloneProxyPublicationState() == cloneProxyReady
}

// initRuntimeProxy 使用完整初始化模式创建独立的 Godot 精灵代理。
//
// 直接调用方：SpriteImpl.init、cloneSprite；顶层入口：项目精灵加载和运行时克隆创建。
// true 表示代理创建后立即提交继承/配置得到的服装与图层，避免首帧显示默认纹理。
func (p *SpriteImpl) initRuntimeProxy() {
	p.rebuildRuntimeProxy(true)
}

// awake 完成精灵在 Go 侧的唤醒步骤。
//
// 直接调用方：cloneSprite，以及 runSpriteCallbacks 登记的 bootstrap 任务；顶层入口
// 分别是运行时克隆创建和项目加载。它在代理已创建后尝试播放默认动画并设置
// IsAwakened；若对象是运行时克隆，最终可见性仍受 publication 状态机控制。
func (p *SpriteImpl) awake() {
	p.animation().playDefaultAnimIfIdle()
	p.spriteState.IsAwakened = true
}

// rebuildRuntimeProxy 清除 Go 侧继承到的代理引用，并在 Godot 主线程建立新代理。
//
// 直接调用方：initRuntimeProxy；顶层入口：项目精灵加载和克隆创建。SyncSprite 的
// 创建以及纹理、物理、信号等节点操作受 Godot 主线程约束，所以统一包在
// engine.WaitMainThread 中。applyCostume 控制新代理建立后是否立刻提交服装；当前完整
// 初始化路径传 true。cloneSprite 会先复制源精灵状态，其中可能带有源 SyncSprite
// 指针，因此创建克隆代理前必须置 nil；这里不会、也不能销毁源精灵的 Godot 节点。
// 当前生产代码只把该方法用于首次初始化，不应把它当成通用的代理热重建接口。
func (p *SpriteImpl) rebuildRuntimeProxy(applyCostume bool) {
	p.runtimeState.SyncSprite = nil
	engine.WaitMainThread(func() {
		p.ensureProxyInitialized()
		if applyCostume {
			p.baseObj.applyCostumeUpdate()
		}
	})
}

// ensureProxyInitialized 在尚无代理时创建并配置 Godot 精灵节点。
//
// 直接调用方：rebuildRuntimeProxy；顶层入口：项目精灵加载和运行时克隆创建。
// BridgeNewBareSprite 只建立基础代理；随后必须按固定顺序补齐物理配置、经过克隆门控
// 计算的可见性、名称/类型、图形效果和动画回调。最后 markProxyDirty，确保下一次
// SpriteSyncBuffer 批处理仍会发送完整变换，而不是把创建时位置当作已同步版本。
// 本函数包含 Godot 节点调用，调用者必须保证当前处于引擎主线程。
func (p *SpriteImpl) ensureProxyInitialized() {
	if p.runtimeState.SyncSprite != nil || p.isDestroyed() {
		return
	}
	p.runtimeState.SyncSprite = engine.BridgeNewBareSprite(p, mathf.NewVec2(p.getXY()))
	p.applyPhysicsProxyConfig()
	p.runtimeState.SyncSprite.SetVisible(p.effectiveProxyVisibility())
	p.runtimeState.SyncSprite.Name = p.name
	p.runtimeState.SyncSprite.SetTypeName(p.name)
	p.applyGraphicEffects(true)
	p.animation().registerOnAnimationLooped(p.handleAnimationLooped)
	p.animation().registerOnAnimationFinished(p.handleAnimationFinished)
	p.markProxyDirty()
}

// effectiveProxyVisibility 计算本次写给 Godot 代理的最终可见性。
//
// 逻辑隐藏始终优先；普通精灵或已公开克隆遵循 IsVisible；Pending/Ready 克隆默认
// 隐藏，只有碰撞感知路径持有临时 visibility lease 时才可见。该值同时用于代理创建、
// 即时查询同步、批量变换同步以及查询租约释放，保证几条写入路径采用同一门控规则。
// 直接调用方：ensureProxyInitialized、ensureProxyQueryStateSynced、appendTransformUpdate
// 和 releasePendingCloneSensingVisibility；顶层覆盖代理创建、查询、帧同步及租约清理。
func (p *SpriteImpl) effectiveProxyVisibility() bool {
	if !p.spriteState.IsVisible {
		return false
	}
	publication := p.proxyPublication
	if publication == nil || atomic.LoadUint32(&publication.state) == cloneProxyPublished {
		return true
	}
	return publication.sensingVisibilityLeases.Load() > 0
}

// finishCloneInitialization 标记克隆首段初始化完成，使其可在下一次代理批次公开。
//
// 直接调用方：dispatchCloneLifecycle 的 defer；顶层入口：运行时克隆的 OnCloned
// 生命周期。CAS 只允许 Pending -> Ready，因而重复回调、已销毁对象或非克隆对象都
// 不会重新发布。这里只登记 ready，不立即 SetVisible；collectProxyUpdate 会先应用
// 服装和图层，再原子切到 Published 并提交可见性，避免任何一帧观察到继承中的旧状态。
func (p *SpriteImpl) finishCloneInitialization() {
	if p.isDestroyed() || p.proxyPublication == nil ||
		!atomic.CompareAndSwapUint32(&p.proxyPublication.state, cloneProxyPending, cloneProxyReady) {
		return
	}
	p.g.shapeMgr.markCloneProxyPublicationReady()
}

// handleAnimationFinished 处理 Godot 动画完成回调，并记录已经完成的动画名。
//
// 直接调用方：ensureProxyInitialized 注册的 SyncSprite 动画完成事件；顶层来源：
// Godot AnimatedSprite2D 的 animation_finished 信号经 native/Web 回调桥进入 Go。
// 回调可能重入运行时共享状态，因此先获取 engine.Lock；代理已销毁或当前没有有效
// 动画名时直接忽略，避免把过期 Godot 信号记到新生命周期中。
func (p *SpriteImpl) handleAnimationFinished() {
	engine.Lock()
	defer engine.Unlock()
	if p.isDestroyed() || p.runtimeState.SyncSprite == nil {
		return
	}
	state := p.animation().getCurAnimState()
	if state != nil && state.Name != "" {
		p.animation().addDoneAnimation(state.Name)
	}
}

// handleAnimationLooped 在 Godot 动画循环边界登记下一轮关联音频。
//
// 直接调用方：ensureProxyInitialized 注册的 SyncSprite 动画循环事件；顶层来源：
// Godot AnimatedSprite2D 的 animation_looped 信号经回调桥进入 Go。帧动画和 tween
// 动画各自可能带循环音频，因此两类当前状态都要入队。这里同样持有 engine.Lock，
// 并在代理已失效时丢弃迟到回调。
func (p *SpriteImpl) handleAnimationLooped() {
	engine.Lock()
	defer engine.Unlock()
	if p.isDestroyed() || p.runtimeState.SyncSprite == nil {
		return
	}
	p.queueAnimationLoopAudio(p.animation().getCurAnimState())
	p.queueAnimationLoopAudio(p.animation().getCurTweenState())
}

// -----------------------------------------------------------------------------
// 精灵代理同步辅助方法
// -----------------------------------------------------------------------------

// applyPhysicsProxyConfig 把 Go 侧物理组件配置应用到已经创建的 Godot 代理。
//
// 直接调用方：ensureProxyInitialized、Game.applyCollisionLayers；顶层入口分别是精灵
// 代理初始化，以及项目物理碰撞层初始化。代理为空时无需处理；实际的模式、碰撞层、
// mask 和形状配置由 physicsComponent 统一下发，避免本同步层复制物理规则。
func (p *SpriteImpl) applyPhysicsProxyConfig() {
	if p.runtimeState.SyncSprite == nil {
		return
	}
	p.physics().applyPhysicsProxyConfig(p.runtimeState.SyncSprite)
}

// shouldPullPhysicsPosition 判断精灵是否应参与本帧 Godot -> Go 的位置回读。
//
// 直接调用方：pullPhysicsPositions 传给 SyncBatchPositions 的过滤器；顶层入口：
// Game.OnEngineUpdate。未创建代理或 NoPhysics 精灵的位置由 Go 逻辑主导，无需向
// Godot 查询；启用物理的精灵则以物理引擎解算后的位置为准。
func (p *SpriteImpl) shouldPullPhysicsPosition() bool {
	return p.runtimeState.SyncSprite != nil && p.PhysicsMode() != NoPhysics
}

// applyPhysicsPosition 把 Godot 物理解算位置写回 Go 侧变换状态。
//
// 直接调用方：pullPhysicsPositions 的批量回读回调；顶层入口：Game.OnEngineUpdate。
// setPositionRaw 只在坐标实际变化时返回 true。这里调用 markVisualDirty 而不是
// markProxyDirty：Godot 本来就是这次位置的来源，若再次标记代理脏会把相同坐标回传
// 给 Godot；但 VisualVersion 仍需递增，让相机、气泡等 Go 侧观察者感知位置变化。
func (p *SpriteImpl) applyPhysicsPosition(x, y float64) {
	if p.transform().setPositionRaw(x, y) {
		p.markVisualDirty()
	}
}

// ensureProxyQueryStateSynced 在同步碰撞/点击查询前，立即把当前 Go 状态写到代理。
//
// 直接调用方：prepareSelfCollisionQuery、Game.pointHitsClickTarget 及相关查询测试；
// 顶层入口：Touching、TouchingColor、鼠标命中和点击目标选择。查询不能等待帧末批次，
// 否则会使用上一帧纹理或变换，因此这里先提交服装，再重建随服装变化的自动物理形状，
// 最后通过 SyncSprite.SetTransform 直接同步变换、渲染偏移与门控后的可见性。
//
// ProxySyncVersion 用于与常规批处理去重：即时写入后记录 DirtyVersion，但不清除
// IsDirty。这样 collectProxyUpdate 仍可完成本轮收尾；若版本未再变化，它不会重复追加
// 相同变换。Godot 节点要求主线程访问；SyncSprite/Manager 的同步包装层会把这些调用
// 切到主线程并等待结果，保证紧随其后的查询能看到刚写入的状态。
func (p *SpriteImpl) ensureProxyQueryStateSynced() {
	if p.isDestroyed() || p.runtimeState.SyncSprite == nil {
		return
	}
	// 即使逻辑上隐藏，感知查询也需要最新服装轮廓；不能沿用帧同步的“隐藏则暂缓服装”优化。
	p.baseObj.applyCostumeUpdate()
	p.syncAutoPhysicsShapesAfterCostumeChange()
	if !p.spriteState.IsDirty {
		return
	}
	if p.spriteState.ProxySyncVersion == p.spriteState.DirtyVersion {
		return
	}

	x, y := p.getXY()
	renderOffsetX, renderOffsetY := getRenderOffset(p)
	rot, scaleX, scaleY := getRenderRotationAndScale(p)

	p.runtimeState.SyncSprite.SetTransform(
		mathf.NewVec2(x, y),
		engine.DegToRad(rot),
		mathf.NewVec2(scaleX, scaleY),
		p.effectiveProxyVisibility(),
		mathf.NewVec2(renderOffsetX, renderOffsetY),
	)
	p.spriteState.ProxySyncVersion = p.spriteState.DirtyVersion
}

// collectProxyUpdate 收集一个精灵在本轮 Go -> Godot 批处理中的变化。
//
// 直接调用方：shapeManager.collectProxyUpdates；顶层入口：OnEngineUpdate 的常规同步
// 和 OnEngineRender 的协程后补同步。已销毁或无代理对象直接跳过。普通隐藏精灵可暂缓
// 服装更新以减少节点操作，但 Ready 克隆必须在公开前强制应用服装/图层；随后同步自动
// 物理形状并打开 publication 门控。
//
// IsDirty 表示本轮尚需收尾，DirtyVersion/ProxySyncVersion 表示变换是否真的需要发送。
// 即时查询可能已经同步相同版本，所以二者要分开判断。Ready 克隆公开时必须再次
// markProxyDirty，确保即便查询路径同步过变换，新的“可见”状态仍会进入批量数据。
func (p *SpriteImpl) collectProxyUpdate(buffer *engine.SpriteSyncBuffer) {
	if p.isDestroyed() || p.runtimeState.SyncSprite == nil {
		return
	}
	publishingClone := p.cloneProxyPublicationState() == cloneProxyReady
	if p.spriteState.IsVisible || publishingClone {
		p.baseObj.applyCostumeUpdate()
	}
	p.syncAutoPhysicsShapesAfterCostumeChange()
	if publishingClone {
		atomic.CompareAndSwapUint32(&p.proxyPublication.state, cloneProxyReady, cloneProxyPublished)
		// 查询同步可能已经把最新逻辑变换写入代理，但当时代理仍受门控而隐藏。
		// 门控打开后强制生成新版本，防止版本合并把这次可见性更新跳过。
		p.markProxyDirty()
	}
	if !p.spriteState.IsDirty {
		return
	}
	if p.spriteState.ProxySyncVersion != p.spriteState.DirtyVersion {
		p.appendTransformUpdate(buffer)
	}
	p.spriteState.IsDirty = false
}

// appendTransformUpdate 把精灵最终渲染变换追加到可复用的 SpriteSyncBuffer。
//
// 直接调用方：collectProxyUpdate；顶层入口：Game.OnEngineUpdate/OnEngineRender 的
// 代理批处理。SPX 对外使用角度制，Godot 使用弧度制，因此 rotation 在此统一转换；
// getRenderRotationAndScale 已合并朝向、服装 faceRight 和旋转样式，并在 LeftRight
// 模式下产生 X 轴翻转；服装尺寸缩放由 applyCostumeUpdate 单独下发。renderOffset
// 则根据服装锚点、pivot 和运行时 Scale 计算 RenderRoot 相对根节点的局部偏移。
// 追加成功后记录 ProxySyncVersion，供即时查询和后续批次判断同一逻辑版本是否已发送。
func (p *SpriteImpl) appendTransformUpdate(buffer *engine.SpriteSyncBuffer) {
	x, y := p.getXY()
	renderOffsetX, renderOffsetY := getRenderOffset(p)
	rot, scaleX, scaleY := getRenderRotationAndScale(p)
	buffer.Add(
		int64(p.runtimeState.SyncSprite.Id),
		x, y,
		engine.DegToRad(rot),
		scaleX, scaleY,
		renderOffsetX, renderOffsetY,
		p.effectiveProxyVisibility(),
	)
	p.spriteState.ProxySyncVersion = p.spriteState.DirtyVersion
}

// isSpriteTouchable 判断物理触发事件中的精灵是否仍可派发触碰事件。
//
// 直接调用方：processPhysicsTriggers 传给 ProcessTriggerPairs 的过滤器；顶层入口：
// internal/engine.onUpdate 的 GameRender 准备阶段。触发信号从产生到消费会跨越帧边界，
// 因此必须再次校验逻辑可见性和 IsDying，避免向已经隐藏或进入销毁流程的精灵派发
// OnTouchStart。该谓词本身不额外调用 isDestroyed。
func isSpriteTouchable(sprite *SpriteImpl) bool {
	return sprite.spriteState.IsVisible && !sprite.spriteState.IsDying
}
