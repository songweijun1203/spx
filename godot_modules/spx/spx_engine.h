/**************************************************************************/
/*  spx_engine.h                                                          */
/**************************************************************************/
/*                         This file is part of:                          */
/*                             GODOT ENGINE                               */
/*                        https://godotengine.org                         */
/**************************************************************************/
/* Copyright (c) 2014-present Godot Engine contributors (see AUTHORS.md). */
/* Copyright (c) 2007-2014 Juan Linietsky, Ariel Manzur.                  */
/*                                                                        */
/* Permission is hereby granted, free of charge, to any person obtaining  */
/* a copy of this software and associated documentation files (the        */
/* "Software"), to deal in the Software without restriction, including    */
/* without limitation the rights to use, copy, modify, merge, publish,    */
/* distribute, sublicense, and/or sell copies of the Software, and to     */
/* permit persons to whom the Software is furnished to do so, subject to  */
/* the following conditions:                                              */
/*                                                                        */
/* The above copyright notice and this permission notice shall be         */
/* included in all copies or substantial portions of the Software.        */
/*                                                                        */
/* THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,        */
/* EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF     */
/* MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. */
/* IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY   */
/* CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,   */
/* TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE      */
/* SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.                 */
/**************************************************************************/

#ifndef SPX_ENGINE_H
#define SPX_ENGINE_H

#include "core/variant/callable.h"
#include "core/string/print_string.h"
#include "scene/main/scene_tree.h"
#include "gdextension_spx_ext.h"
#include "spx_manager.h"

class SceneTree;
class Window;
class Node;
class Image;
class TextureRect;
class CanvasLayer;
class SpxInputMgr;
class SpxAudioMgr;
class SpxPhysicsMgr;
class SpxSpriteMgr;
class SpxUiMgr;
class SpxSceneMgr;
class SpxCameraMgr;
class SpxPlatformMgr;
class SpxResMgr;
class SpxDebugMgr;
class SpxNavigationMgr;
class SpxPenMgr;
class SpxTilemapMgr;
class SpxTilemapparserMgr;
class SpxCallbackProxy;

// msg is borrowed for this synchronous callback; copy it to retain it, and do not free it.
// msg 仅在同步回调期间借用；需要跨回调保存时必须复制，调用方不得释放该指针。
typedef void (*GDExtensionSpxGlobalRuntimePanicCallback)(GdString msg);
typedef void (*GDExtensionSpxGlobalRuntimeExitCallback)(GdInt code);
typedef void (*GDExtensionSpxGlobalRuntimeResetCallback)(GdInt code);

// SPX 在 Godot 侧的唯一运行时协调器。
// 负责持有所有 Manager、保存 Go/JS 回调表、连接 SceneTree 生命周期，并协调暂停、重置、
// 单帧执行和重置期间的末帧冻结。实例由 register_callbacks() 创建、shutdown() 销毁；
// 场景对象只能在 Godot 主线程访问，跨线程控制必须先经过 SpxPendingControls。
// 顶层调用链：Go/Web API 或 Godot 主循环 -> Spx/SpxExtMgr -> SpxEngine -> Managers/Go callbacks。
class SpxEngine {
	// 进程内唯一实例；由本类独占所有权，置空后任何 Manager 宏和回调都不得再访问。
	static inline SpxEngine *singleton = nullptr;

public:
	// 返回借用单例；调用方必须先用 is_initialized() 判断，且不得跨 shutdown 保存。
	static SpxEngine *get_singleton() { return singleton; }
	static bool is_initialized() { return singleton != nullptr; }
	// 安装完整 C++ -> Go/JS 回调表并创建单例；直接调用方是 Native/Web 初始化桥。
	static void register_callbacks(GDExtensionSpxCallbackInfoPtr callback);
	// 销毁单例；直接调用方：Spx::on_destroy()，顶层调用方：Godot 主循环销毁流程。
	static void shutdown();
	static void register_runtime_panic_callbacks(GDExtensionSpxGlobalRuntimePanicCallback callback);
	static void register_runtime_exit_callbacks(GDExtensionSpxGlobalRuntimeExitCallback callback);
	static void register_runtime_reset_callbacks(GDExtensionSpxGlobalRuntimeResetCallback callback);
	~SpxEngine() = default;

private:
	// Manager 的拥有型列表；创建顺序保存、销毁时逆序 memdelete，避免依赖方先于被依赖方释放。
	Vector<SpxManager *> managers;
	// 以下均为 managers 中对象的非拥有型快捷指针，只能在 singleton 生命周期内使用。
	// 输入状态与 InputMap 适配器；仅 Godot 主线程访问场景输入对象。
	SpxInputMgr *input = nullptr;
	// 音频播放与总线管理器。
	SpxAudioMgr *audio = nullptr;
	// 物理查询、碰撞和触发器管理器。
	SpxPhysicsMgr *physics = nullptr;
	// 精灵节点、渲染与批量同步管理器。
	SpxSpriteMgr *sprite = nullptr;
	// SPX UI 控件管理器。
	SpxUiMgr *ui = nullptr;
	// 场景和舞台背景管理器。
	SpxSceneMgr *scene = nullptr;
	// Camera2D 管理器。
	SpxCameraMgr *camera = nullptr;
	// 窗口、时间及平台能力管理器。
	SpxPlatformMgr *platform = nullptr;
	// 纹理、字体、音频等资源加载/缓存管理器。
	SpxResMgr *res = nullptr;
	// 调试模式与调试绘制管理器。
	SpxDebugMgr *debug = nullptr;
	// 网格和路径查询管理器。
	SpxNavigationMgr *navigation = nullptr;
	// Scratch/SPX 画笔命令管理器。
	SpxPenMgr *pen = nullptr;
	// 运行时 TileMap 管理器。
	SpxTilemapMgr *tilemap = nullptr;
	// 地图数据解析管理器。
	SpxTilemapparserMgr *tilemapparser = nullptr;

	// 挂入 SceneTree 的回调代理节点，借助 Godot signal 调度延迟 reset；SceneTree 拥有节点。
	SpxCallbackProxy *delay_proxy = nullptr;

	// 创建一个由 managers 拥有的 Manager，并返回仅在引擎生命周期内有效的借用指针。
	template <typename T>
	T *_create_manager() {
		T *mgr = memnew(T);
		managers.append(mgr);
		return mgr;
	}

	void _destroy_all_managers();

public:
	// 以下访问器返回 managers 中对象的借用指针，不转移所有权，也不得跨 shutdown 保存。
	SpxInputMgr *get_input() { return input; }
	SpxAudioMgr *get_audio() { return audio; }
	SpxPhysicsMgr *get_physics() { return physics; }
	SpxSpriteMgr *get_sprite() { return sprite; }
	SpxUiMgr *get_ui() { return ui; }
	SpxSceneMgr *get_scene() { return scene; }
	SpxCameraMgr *get_camera() { return camera; }
	SpxPlatformMgr *get_platform() { return platform; }
	SpxResMgr *get_res() { return res; }
	SpxDebugMgr *get_debug() { return debug; }
	SpxNavigationMgr *get_navigation() { return navigation; }
	SpxPenMgr *get_pen() { return pen; }
	SpxTilemapMgr *get_tilemap() { return tilemap; }
	SpxTilemapparserMgr *get_tilemapparser() { return tilemapparser; }

private:
	// 当前 Godot SceneTree 的借用指针；由 Godot 主循环拥有，只在 set_root_node 后有效。
	SceneTree *tree = nullptr;
	// SPX 自建场景子树的锚点；SceneTree 拥有，SpxEngine 仅借用。
	Node *spx_root = nullptr;
	// 发给 Go 的对象 ID 单调计数器；只在 Godot 主线程递增，0 保留作空对象 ID。
	GdInt global_id = 0;
	// C++ -> Go/JS 的函数表副本；函数指针的实现与生命周期由桥接层负责。
	SpxCallbackInfo callbacks = {};
	// 宿主 panic 通知的借用函数指针，仅同步调用；字符串参数只在调用期间有效。
	GDExtensionSpxGlobalRuntimePanicCallback on_runtime_panic = nullptr;
	// 宿主退出通知的借用函数指针，仅同步调用，不拥有用户数据。
	GDExtensionSpxGlobalRuntimeExitCallback on_runtime_exit = nullptr;
	// 宿主 reset 完成通知的借用函数指针，仅同步调用，不拥有用户数据。
	GDExtensionSpxGlobalRuntimeResetCallback on_runtime_reset = nullptr;

	template <typename Callback>
	// 在单例存在时替换一个宿主回调；调用方负责保证注册与 runtime 调用不并发。
	static void _register_runtime_callback(
			Callback SpxEngine::*member,
			Callback callback,
			const char *func_name) {
		if (singleton == nullptr) {
			print_error(vformat("%s failed, engine not initialized!", func_name));
			return;
		}

		singleton->*member = callback;
	}

	// 重置等待期覆盖在最上层的画布；加入 SceneTree 后由 Godot 拥有，释放使用 queue_free()。
	CanvasLayer *freeze_layer = nullptr;
	// 展示最后一帧截图的 TextureRect；由 freeze_layer/SceneTree 拥有。
	TextureRect *freeze_screen = nullptr;
	// 冻结覆盖层是否已经建立，防止重复截图和重复挂接节点。
	bool is_frozen_frame = false;

	// Godot 创建的一次性 SceneTreeTimer 强引用；用于延迟通知宿主完成 reset。
	Ref<SceneTreeTimer> reset_timer;
	// 连接 reset_timer.timeout 的 Callable，目标是 delay_proxy；断连前必须保持同一实例。
	Callable on_timeout_callable;
	// Web reset 前保留最后一帧的固定等待秒数。
	static constexpr double RESET_PAUSE_DELAY_SEC = 5.0;
	// 是否启用上述延迟；由平台启动配置设置，主线程读取。
	bool should_delay_runtime_reset = false;

	// Manager 是否已完成 on_awake/on_start，用于保证启动/销毁通知成对且幂等。
	bool managers_awake = false;
	// shutdown 重入保护；置位后不允许再次开始销毁。
	bool shutting_down = false;
	// 是否已进入永久退出流程；退出后物理帧、逻辑帧和 runtime 回调均停止。
	bool has_exit = false;
	// 当前是否处于“本局已重置、底层引擎待复用”状态。
	bool is_spx_reset = true;
	// SPX 视角的暂停状态；与 SceneTree::paused 协调但单独保存供跨线程查询镜像。
	bool is_spx_paused = false;
	// 暂停状态下是否临时放行下一逻辑/物理帧；完成一帧后自动清除。
	bool should_execute_single_frame = false;

public:
	SpxCallbackInfo *get_callbacks();
	GDExtensionSpxGlobalRuntimePanicCallback get_on_runtime_panic() { return on_runtime_panic; }
	GDExtensionSpxGlobalRuntimeExitCallback get_on_runtime_exit() { return on_runtime_exit; }
	GDExtensionSpxGlobalRuntimeResetCallback get_on_runtime_reset() { return on_runtime_reset; }

public:
	// 生命周期入口均由 Spx 直接调用；顶层调用方是 Godot 主循环或 Go/Web 控制 API。
	// Godot 规定：涉及 SceneTree、Viewport、Node 的调用必须发生在主线程。
	GdInt get_unique_id();
	Node *get_spx_root();
	SceneTree *get_tree();
	Window *get_root();
	void set_root_node(SceneTree *p_tree, Node *p_node);

	void on_awake();
	void on_fixed_update(float delta);
	void on_update(float delta);
	void on_destroy();
	void on_exit(int exit_code);
	void on_reset(int reset_code);

	bool is_reset();
	void restart();
	void set_delay_runtime_reset(bool p_delay);

	void capture_last_frame();
	void clear_frozen_frame();

	void pause();
	void resume();
	bool is_paused() const;
	void next_frame();

private:
	void _do_reset(int reset_code);
	void _invoke_runtime_reset(int reset_code);
	void _invoke_runtime_reset_delayed(int reset_code);
	void _disconnect_reset_timer();

	void _on_godot_pause_changed(bool is_godot_paused);
	void _set_paused_pure(bool p_paused);

	Ref<Image> _get_viewport_image() const;
	TextureRect *_create_freeze_texture(const Ref<Image> &img) const;
	void _attach_freeze_node(TextureRect *screen);

	void _initialize_managers();
	// 按 managers 保存顺序同步广播生命周期钩子；必须在 Godot 主线程调用。
	template <typename... Args>
	void _notify_managers(void (SpxManager::*p_callback)(Args...), Args... p_args) {
		for (SpxManager *manager : managers) {
			(manager->*p_callback)(p_args...);
		}
	}
};

#endif // SPX_ENGINE_H
