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
	"maps"
	"reflect"

	coreproject "github.com/goplus/spx/v3/internal/core/project"
	corestate "github.com/goplus/spx/v3/internal/core/state"
	spxlog "github.com/goplus/spx/v3/internal/log"
)

// SpriteImpl 是 Sprite 接口的核心实现，也是所有用户精灵结构体都要嵌入的
// 运行时基类。XGo 生成的具体精灵负责保存用户字段和 Main/事件注册代码，
// SpriteImpl 则统一管理造型、事件 owner、组件、生命周期以及 Godot 代理同步。
//
// 这里有意同时保存“用户精灵对象”和“引擎运行状态”：sprite 指回外层具体
// 精灵，用于调用 Main、克隆完整用户对象；其余字段只描述 SPX 内部状态。
type SpriteImpl struct {
	// baseObj 提供造型列表、当前造型、图形特效、渲染层及 Godot SyncSprite
	// 代理等 Sprite/Backdrop 共用能力。匿名嵌入后，SpriteImpl 可直接调用这些方法。
	baseObj
	// scriptEventBindings 把本精灵绑定为事件 owner。OnClick、OnCloned、OnMsg
	// 等 API 通过它把 handler 注册到所属 Game 的统一事件表中。
	scriptEventBindings

	// sprite 是包含当前 SpriteImpl 的外层具体精灵对象，而不是 Godot 代理。
	// 例如用户的 Kai 结构体实现 Sprite 后，这里保存的是 *Kai；克隆时据此
	// 反射复制 Kai 的用户字段，并重新执行 Kai.Main 来登记克隆自己的事件。
	sprite Sprite
	// spriteState 保存轻量生命周期/同步标志，例如可见、克隆、死亡、唤醒，
	// 以及 Go 状态与 Godot 代理之间的脏版本。具体位置、动画、画笔等状态由
	// components 或匿名嵌入的 baseObj 分别管理。
	spriteState corestate.SpriteRuntimeState
	// proxyPublication 只用于运行时克隆的代理发布状态。新克隆先创建为隐藏，
	// 等 OnCloned 的首段初始化代码执行完成后再发布，避免屏幕短暂显示继承自
	// 原精灵、但尚未来得及修改的造型或位置。指针在克隆构造后保持稳定，防止
	// 反射复制正在使用的发布状态。
	proxyPublication *cloneProxyPublication
	// name 是项目配置中的精灵名称，也是查找精灵、代理命名和调试日志的标识。
	name string
	// components 将变换、动画、物理、画笔、声音和气泡拆成独立状态机；除气泡
	// 按需创建外，其余组件在 initComponents 中随 SpriteImpl 一起初始化。
	components spriteComponents

	// g 指向所属游戏运行时，提供事件表、形状管理器、输入、声音及引擎管理器。
	// 所有组件都通过 SpriteImpl 间接访问它，不单独持有 Game。
	g *Game
	// gamer 是加载精灵时传入的顶层用户 Game 反射值；它与 g（内部运行时）
	// 含义不同，保留的是用户声明出来的完整游戏结构体。
	gamer reflect.Value
}

// -----------------------------------------------------------------------------
// Public API
// -----------------------------------------------------------------------------
func (p *SpriteImpl) Name() string {
	// 名称在加载项目时确定，克隆沿用原精灵名称。
	return p.name
}

func (p *SpriteImpl) IsCloned() bool {
	return p.spriteState.Cloned
}

// InitFrom 在反射克隆复制完整用户精灵结构后，重新建立 SpriteImpl 的内部状态。
// 用户字段已由 cloneSprite 的 out.Set(in) 复制；这里不能照搬会与原对象共享的
// 事件 owner、代理同步版本和克隆发布状态。各功能组件随后由 cloneFrom 单独
// 创建，确保画笔、动画、物理等组件仍指向新的 SpriteImpl。
func (p *SpriteImpl) InitFrom(src *SpriteImpl) {
	// 造型资源可只读共享，但当前造型和图形特效状态属于克隆自己。
	p.baseObj.initFrom(&src.baseObj)
	// 复用同一个 Game 事件注册表，同时把 owner 改为新克隆 p。
	p.scriptEventBindings.initFrom(&src.scriptEventBindings, p)

	// g 和 name 沿用来源精灵；Scale、图形特效复制当前值，而不是重新读取配置。
	p.g, p.name, p.runtimeState.Scale = src.g, src.name, src.runtimeState.Scale
	p.greffUniforms = maps.Clone(src.greffUniforms)

	// 克隆继承可见性，但生命周期从“尚未唤醒、未死亡”的新对象重新开始。
	p.spriteState.IsVisible = src.spriteState.IsVisible
	p.spriteState.Cloned = true
	p.spriteState.IsDying = false
	p.spriteState.IsAwakened = false

	// 新对象尚未把任何状态同步到自己的 Godot 代理，因此版本与事件能力缓存
	// 必须清零；Main 中对应的事件注册入口会再按需设置相关 HasOn* 标志。
	p.spriteState.DirtyVersion = 0
	p.spriteState.ProxySyncVersion = 0
	p.proxyPublication = nil
	p.spriteState.HasOnCloned = false
	p.spriteState.HasOnTouchStart = false
	p.spriteState.HasOnTouching = false
	p.spriteState.HasOnTouchEnd = false
}

func (p *SpriteImpl) Die() {
	// Die 与 Destroy 的区别是：先进入 dying 状态，使碰撞/动画等逻辑不再把它
	// 当作普通活动精灵，然后停止同精灵的其他脚本，等待死亡动画后再销毁。
	p.setDying()
	p.Stop(OtherScriptsInSprite)
	p.playStateAnimationAndWait(StateDie)
	p.Destroy()
}

func (p *SpriteImpl) Destroy() {
	if isDebugInstrEnabled() {
		spxlog.Debug("Destroy: %s", p.name)
	}
	// 先解除可见对象、组件和事件资源，再停止该精灵的全部脚本。若 Destroy
	// 正由这个精灵自己的协程调用，最后还要中止当前协程，防止销毁后的语句继续执行。
	p.teardown()
	p.Stop(ThisSprite)
	p.markDestroyed()
	p.abortIfCurrentCoroutine()
}

func (p *SpriteImpl) DeleteThisClone() {
	// 与 Scratch 一致：原始精灵调用“删除此克隆”没有效果。
	if !p.spriteState.Cloned {
		return
	}
	p.Destroy()
}

// -----------------------------------------------------------------------------
// Internal State
// -----------------------------------------------------------------------------
func (p *SpriteImpl) setDying() {
	// IsDying 与永久销毁标志不同：死亡动画播放期间对象仍存在，但会从部分
	// 碰撞/动画候选中过滤，直到 Destroy 完成最终清理。
	p.spriteState.IsDying = true
}

func (p *SpriteImpl) markProxyDirty() {
	// 可见精灵的位置、方向、大小、造型等发生变化时，记录“本帧需要重绘”。
	// 这个标记不仅用于渲染，也会被 queueNextLoopRound 读取：一旦本帧请求
	// 过重绘，普通 forever 到达循环边界后就留到下一引擎帧再继续。
	// 隐藏精灵不影响当前画面，因此只保留脏状态，不要求结束同帧循环轮次。
	p.requestRedrawIfVisible()
	p.spriteState.DirtyVersion++
	p.spriteState.IsDirty = true
}

// -----------------------------------------------------------------------------
// Initialization
// -----------------------------------------------------------------------------
func (p *SpriteImpl) init(
	g *Game, name string, spriteCfg *coreproject.SpriteConfig, gamer reflect.Value, sprite Sprite) {
	// 初始化顺序体现依赖关系：组件需要基础造型和 Game/精灵引用，原生代理
	// 又需要组件中的位置、物理及动画配置，因此不可随意交换这四步。
	p.initBaseObjects(spriteCfg, g)
	p.initBasicProperties(g, name, sprite, gamer, spriteCfg)
	p.initComponents(spriteCfg)
	p.initRuntimeProxy()
}

func (p *SpriteImpl) initBaseObjects(spriteCfg *coreproject.SpriteConfig, g *Game) {
	// 普通逐造型配置与合图/多部件造型配置走不同加载入口，最终都填充
	// baseObj.costumes 和 costumeIndex。
	if spriteCfg.Costumes != nil {
		p.baseObj.init(spriteCfg.Costumes, spriteCfg.GetCostumeIndex())
	} else {
		p.baseObj.initWith(spriteCfg)
	}
	p.spriteState.DefaultCostumeIndex = p.baseObj.costumeIndex
	// 此时只绑定事件注册表和 owner；用户在 Main 中调用 OnStart/OnClick 等时
	// 才会真正创建 eventSink。
	p.scriptEventBindings.init(&g.scriptEvents, p)
}

func (p *SpriteImpl) initBasicProperties(g *Game, name string, sprite Sprite, gamer reflect.Value, spriteCfg *coreproject.SpriteConfig) {
	// sprite 与 gamer 都是用户层对象；g 是嵌入其中、实际驱动引擎的 *Game。
	p.gamer = gamer
	p.g, p.name, p.sprite = g, name, sprite
	p.runtimeState.Scale = spriteCfg.Size
	p.spriteState.IsVisible = spriteCfg.Visible
	p.spriteState.IsAwakened = false
}

func (p *SpriteImpl) initComponents(spriteCfg *coreproject.SpriteConfig) {
	// 每个非克隆精灵从项目配置创建独立组件；克隆走 spriteComponents.cloneFrom。
	p.components.initComponents(p, spriteCfg)
}

// -----------------------------------------------------------------------------
// Components
// -----------------------------------------------------------------------------
// 以下访问器将 SpriteImpl 的对外 API 转发到对应组件，避免调用方直接依赖
// spriteComponents 的字段布局。返回值始终属于当前精灵，不能在精灵间复用。
func (p *SpriteImpl) transform() *transformComponent {
	return p.components.getTransform()
}

func (p *SpriteImpl) animation() *animationComponent {
	return p.components.getAnimation()
}

func (p *SpriteImpl) physics() *physicsComponent {
	return p.components.getPhysics()
}

func (p *SpriteImpl) pen() *penComponent {
	return p.components.getPen()
}

func (p *SpriteImpl) sound() *soundComponent {
	return p.components.getSound()
}

// -----------------------------------------------------------------------------
// Lifecycle Helpers
// -----------------------------------------------------------------------------

func (p *SpriteImpl) playStateAnimationAndWait(stateName string) {
	// 状态名（如 die）先映射到项目配置的动画名；未配置时视为无需等待，
	// 让调用方可以继续完成销毁等生命周期步骤。
	animName := p.getStateAnimName(stateName)
	if animName == "" || !p.hasAnim(animName) {
		return
	}
	p.AnimateAndWait(animName)
}

func (p *SpriteImpl) teardown() {
	// 销毁顺序从用户可见层逐步深入运行时：
	// 1. 停止气泡并隐藏精灵，避免后续帧继续显示；
	// 2. 删除以本精灵为 owner 的事件 sink；
	// 3. 释放画笔、声音等组件持有的后端资源；
	// 4. 从 shapeManager 延迟移除，保证当前帧遍历中的切片仍然有效；
	// 5. 清理点击节流记录，防止代理 ID 的旧状态残留。
	if bubble := p.components.bubble; bubble != nil {
		bubble.stopAll()
	}
	p.setVisible(false)
	p.doDeleteClone()
	p.components.destroyComponents()
	p.g.removeShape(p)
	if syncSprite := p.runtimeState.SyncSprite; syncSprite != nil {
		p.g.inputMgr.removeClickTarget(syncSprite.GetId())
	}
}

func (p *SpriteImpl) abortIfCurrentCoroutine() {
	// Destroy 可能从本精灵自己的事件 handler 内调用。只有当前协程的 owner
	// 就是 p 时才 Abort；其他精灵销毁 p 时不能误杀调用者的协程。
	if gco.IsInCoroutine() {
		current := gco.Current()
		if current != nil && p == current.Obj {
			gco.Abort()
		}
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------
func spriteOf(sprite Sprite) *SpriteImpl {
	// 用户精灵通常形如 `struct { SpriteImpl; *MyGame; ... }`。接口里保存的是
	// *用户精灵，运行时则经常需要取回其中嵌入的 SpriteImpl，所以在顶层字段
	// 中按精确类型查找。这里不递归，生成代码约定 SpriteImpl 必须直接嵌入。
	vSpr := reflect.ValueOf(sprite)
	if vSpr.Kind() == reflect.Pointer {
		vSpr = vSpr.Elem()
	}
	// 非结构体实现不符合生成精灵布局，返回 nil 交由调用方处理。
	if vSpr.Kind() != reflect.Struct {
		return nil
	}
	for i, n := 0, vSpr.NumField(); i < n; i++ {
		fld := vSpr.Field(i)
		if fld.Kind() == reflect.Struct && fld.Type() == reflect.TypeOf(SpriteImpl{}) {
			return fld.Addr().Interface().(*SpriteImpl)
		}
	}
	return nil
}
