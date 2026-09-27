// SPX 自有的 Emscripten JavaScript 桥接库。
// 直接调用方：Godot C++ 中声明的 godot_js_spx_* 导入函数；
// 顶层来源：Godot 生命周期/输入/碰撞/UI 事件，以及浏览器宿主的文件和 reset 请求。
// Emscripten 规则：$GodotGdspx 是库内部单例，__deps 声明链接依赖，__postset 在
// Module 初始化后执行；每个 __sig 描述 WASM 签名，__proxy:'sync' 在线程构建中
// 把涉及 DOM/浏览器状态的调用同步代理到浏览器主线程。
const GodotGdspx = {
	$GodotGdspx__deps: ['$GodotRuntime', '$GodotFS', '$GodotDisplayScreen'],
	// Emscripten 完成 JS Library 合并后执行，把 SPX 文件系统/线程辅助函数挂到 Module。
	// 最近调用方：Emscripten 生成的 Godot Module 初始化代码；最顶层入口：Engine.init() -> Godot(gdmodule)。
	$GodotGdspx__postset: [
		'Module["getPThread"] = GodotGdspx.getPThread;',
		'Module["deleteDirFS"] = GodotGdspx.removeDir;',
		'Module["deleteDirRecursive"] = GodotGdspx.removeDirRecursive;',
		'Module["copyToAdapter"] = GodotGdspx.copyToAdapter;',
		'Module["updateGameDatas"] = GodotGdspx.updateGameDatas;',
		'Module["readAllFS"] = GodotGdspx.readAll;',
		'Module["getFileSize"] = GodotGdspx.getFileSize;',
		'Module["request_reset"] = function () { GodotGdspx.requestReset(); };',
	].join(''),
	$GodotGdspx: {
		// 接触事件类型值到 Go 回调名的固定映射；队列中的 type 从 1 开始。
		contactCallbackEventNames: [
			"OnCollisionEnter",
			"OnCollisionStay",
			"OnCollisionExit",
			"OnTriggerEnter",
			"OnTriggerStay",
			"OnTriggerExit",
		],
		// 队列按每事件 5 个 uint32 槽存储，达到该槽位数时只警告一次。
		contactEventWarnThreshold: 4096 * 5,
		// 宿主最近一次提供的游戏数据；JS 对象拥有数组引用，注册回调后立即补发。
		gameDatas: null,
		// 包装后的 C++ 函数指针；调用结束前临时 UTF-8 内存由包装函数分配并释放。
		gameDataCallback: null,
		// 浏览器宿主触发 C++ reset 的函数；注册前为空操作，避免启动竞态。
		requestReset: function () {},

		getPThread: function () {
			return typeof PThread !== 'undefined' ? PThread : null;
		},

		updateGameDatas: function (path, files) {
			GodotGdspx.gameDatas = { path: path, files: files };
			if (GodotGdspx.gameDataCallback) {
				GodotGdspx.gameDataCallback(path, files);
			}
		},

		copyToAdapter: function (path, adapter) {
			// FS 数据属于 Emscripten 虚拟文件系统；adapter.writeFile 若异步消费，
			// 应由 adapter 自行复制/接管传入 Uint8Array。
			const promises = [];
			const entries = FS.readdir(path).filter(function (value) {
				return value !== '.' && value !== '..';
			});
			entries.forEach(function (entry) {
				const childPath = `${path}/${entry}`;
				const stat = FS.stat(childPath);
				if (FS.isFile(stat.mode)) {
					promises.push(adapter['writeFile'](childPath, FS.readFile(childPath)));
				} else if (FS.isDir(stat.mode)) {
					promises.push(...GodotGdspx.copyToAdapter(childPath, adapter));
				}
			});
			return promises;
		},

		removeDir: function (path) {
			const analysis = FS.analyzePath(path);
			if (analysis.exists && analysis.object && FS.isDir(analysis.object.mode)) {
				FS.rmdir(path);
			}
		},

		removeDirRecursive: function (path) {
			try {
				const stat = FS.stat(path);
				if (!FS.isDir(stat.mode)) {
					FS.unlink(path);
					return;
				}

				const entries = FS.readdir(path).filter((name) => name !== '.' && name !== '..');
				for (const entry of entries) {
					GodotGdspx.removeDirRecursive(`${path}/${entry}`);
				}
				FS.rmdir(path);
			} catch (error) {
				if (error.errno !== GodotFS.ENOENT) {
					GodotRuntime.error(`Failed to remove ${path}`, error);
				}
			}
		},

		readAll: function (path) {
			try {
				const stat = FS.stat(path);
				if (!FS.isFile(stat.mode)) {
					throw new Error(`Path is not a file: ${path}`);
				}
				return FS.readFile(path);
			} catch (error) {
				GodotRuntime.error(`Failed to read file: ${path}`, error);
				return null;
			}
		},

		getFileSize: function (path) {
			return FS.stat(path).size;
		},

		// Go 的 syscall/js 桥把 int64 表示成 low/high 两个 uint32 数值。
		splitInt64: function (value) {
			return {
				'low': Number(value & 0xffffffffn),
				'high': Number((value >> 32n) & 0xffffffffn),
			};
		},

			// 将一个已经转成 JavaScript 类型的 Godot 事件交给 Go WASM。
			// 最近调用方：本文件的 godot_js_spx_on_* 回调；最顶层来源：Godot C++ 生命周期或游戏事件。
			dispatch: function (eventName, ...args) {
			const ffi = globalThis['FFI'];
			if (ffi) {
				ffi['gdspx_dispatch'](eventName, ...args);
			}
		},

		notifyRuntime: function (name, ...args) {
			const ffi = globalThis['FFI'];
			if (ffi && typeof ffi[name] === 'function') {
				ffi[name](...args);
			}
		},

		// 待交给 Go 的扁平接触事件槽位；JS 数组拥有这些 Number，flush 后整体换新。
		contactEvents: [],
		// false 时丢弃 reset/destroy 之后仍迟到的 Godot 接触回调。
		contactSessionActive: true,
		// 每次会话切换递增；逐事件 fallback 用它中止跨会话的旧批次分发。
		contactSessionGeneration: 0,

		setContactSessionActive: function (active) {
			GodotGdspx.contactEvents = [];
			GodotGdspx.contactSessionGeneration += 1;
			GodotGdspx.contactSessionActive = active;
		},

		endContactSession: function (eventName) {
			GodotGdspx.setContactSessionActive(false);
			if (typeof globalThis['GdspxFlushDeferredFrees'] === 'function') {
				globalThis['GdspxFlushDeferredFrees']();
			}
			GodotGdspx.dispatch(eventName);
		},

		queueContact: function (type, selfID, otherID) {
			if (!GodotGdspx.contactSessionActive) {
				return;
			}
			const self = GodotGdspx.splitInt64(selfID);
			const other = GodotGdspx.splitInt64(otherID);
			const events = GodotGdspx.contactEvents;
			const belowWarningThreshold = events.length < GodotGdspx.contactEventWarnThreshold;
			events.push(type, self['low'], self['high'], other['low'], other['high']);
			if (belowWarningThreshold && events.length >= GodotGdspx.contactEventWarnThreshold) {
				GodotRuntime.error("gdspx contact event queue is growing large before flush.");
			}
		},

		flushContactEvents: function () {
			// 直接调用方：engine update/fixed update；顶层为 Godot 每帧主循环。
			const generation = GodotGdspx.contactSessionGeneration;
			const events = GodotGdspx.contactEvents;
			if (events.length === 0) {
				return;
			}
			GodotGdspx.contactEvents = [];

			const batch = globalThis['gdspx_on_contact_events'];
			if (typeof batch === 'function') {
				// Uint32Array 先编码稳定的 5 槽记录，再以字节视图交给 Go 批量解码。
				batch(new Uint8Array(Uint32Array.from(events).buffer));
				return;
			}

			for (let i = 0; i + 4 < events.length && generation === GodotGdspx.contactSessionGeneration; i += 5) {
				const type = events[i] | 0;
				const self = { 'low': events[i + 1] >>> 0, 'high': events[i + 2] >>> 0 };
				const other = { 'low': events[i + 3] >>> 0, 'high': events[i + 4] >>> 0 };
				const eventName = GodotGdspx.contactCallbackEventNames[type - 1];
				if (eventName) {
					GodotGdspx.dispatch(eventName, self, other);
				}
			}
		},
	},

	godot_js_spx_request_reset_cb__proxy: 'sync',
	godot_js_spx_request_reset_cb__sig: 'vi',
	godot_js_spx_request_reset_cb: function (callback) {
		// GodotRuntime.get_func 从 WASM table 解析 C++ 函数指针；Module 存活期内有效。
		GodotGdspx.requestReset = GodotRuntime.get_func(callback);
	},

	godot_js_spx_game_data_cb__proxy: 'sync',
	godot_js_spx_game_data_cb__sig: 'vi',
	godot_js_spx_game_data_cb: function (callback) {
		const func = GodotRuntime.get_func(callback);
		GodotGdspx.gameDataCallback = function (path, files) {
			const args = files || [];
			if (!args.length) {
				return;
			}
			const pathPtr = GodotRuntime.allocString(path);
			const argv = GodotRuntime.allocStringArray(args);
			// C++ callback 必须同步复制；函数返回后立即归还 pathPtr/argv 线性内存。
			func(pathPtr, argv, args.length);
			GodotRuntime.freeStringArray(argv, args.length);
			GodotRuntime.free(pathPtr);
		};
		if (GodotGdspx.gameDatas) {
			GodotGdspx.gameDataCallback(GodotGdspx.gameDatas.path, GodotGdspx.gameDatas.files);
		}
	},

	godot_js_spx_window_size_get__proxy: 'sync',
	godot_js_spx_window_size_get__sig: 'vii',
	godot_js_spx_window_size_get: function (widthPtr, heightPtr) {
		// widthPtr/heightPtr 是 C++ 栈上的 int32 输出槽。线程构建时 __proxy:'sync'
		// 在浏览器主线程读取 window，同时通过共享 WebAssembly.Memory 写回结果。
		const scale = GodotDisplayScreen.getPixelRatio();
		GodotRuntime.setHeapValue(widthPtr, Math.floor(window.innerWidth * scale), 'i32');
		GodotRuntime.setHeapValue(heightPtr, Math.floor(window.innerHeight * scale), 'i32');
	},

	// 接触事件会话边界；runtime reset 后重新启动时还会再调用一次。
	godot_js_spx_contact_session_start__sig: 'v',
	godot_js_spx_contact_session_start: function () {
		GodotGdspx.setContactSessionActive(true);
	},

	// Godot 启动阶段的 SPX 回调入口。
	// C++ 侧在 godot_js_spx_callback.cpp 中把 on_engine_start 回调注册为
	// godot_js_spx_on_engine_start；Godot 开始运行时会从 WASM 调到这里。
	// 最近调用方：SpxEngine::on_awake()；最顶层入口：Godot SceneTree 主循环 start 阶段。
	godot_js_spx_on_engine_start__sig: 'v',
	godot_js_spx_on_engine_start: async function () {
		// 先开启接触事件会话，避免 Go WASM 启动后丢失碰撞事件。
		GodotGdspx.setContactSessionActive(true);
		// Go WASM 尚未完成初始化前，暂时不让 dispatch 使用旧的 FFI。
		globalThis['FFI'] = null;
		// initExtensionWasm 由 webworker/go.wasm.loader.js 暴露到当前 Worker 的
		// self 上。普通模式也可以提供同名入口，但 Worker 模式由它加载 ispx.wasm。
		if (typeof self['initExtensionWasm'] === 'function') {
			await self['initExtensionWasm']();
			return;
		}
		GodotRuntime.error('Missing self.initExtensionWasm for gdspx web callbacks.');
	},

	// 最近调用方：SpxEngine::on_update()；最顶层来源：Godot 每个逻辑/渲染帧。
	godot_js_spx_on_engine_update__sig: 'vf',
	godot_js_spx_on_engine_update: function (delta) {
		// 每个逻辑 Update 统一回收一次临时数组，覆盖此前可能发生的多次 FixedUpdate。
		if (typeof globalThis['GdspxFlushDeferredFrees'] === 'function') {
			globalThis['GdspxFlushDeferredFrees']();
		}
		GodotGdspx.flushContactEvents();
		GodotGdspx.dispatch("OnEngineUpdate", delta);
	},

	// 最近调用方：SpxEngine::on_fixed_update()；最顶层来源：Godot 每个物理帧。
	godot_js_spx_on_engine_fixed_update__sig: 'vf',
	godot_js_spx_on_engine_fixed_update: function (delta) {
		GodotGdspx.flushContactEvents();
		GodotGdspx.dispatch("OnEngineFixedUpdate", delta);
	},

	// 最近调用方：SpxEngine::shutdown()；最顶层来源：Godot 主循环/模块销毁。
	godot_js_spx_on_engine_destroy__sig: 'v',
	godot_js_spx_on_engine_destroy: function () {
		GodotGdspx.endContactSession("OnEngineDestroy");
	},

	// 最近调用方：SpxEngine::shutdown() 完成 C++ 清理后；最顶层来源：Godot 主循环销毁。
	godot_js_spx_on_engine_destroyed__sig: 'v',
	godot_js_spx_on_engine_destroyed: function () {
		GodotGdspx.dispatch("OnEngineDestroyed");
	},

	// 最近调用方：SpxEngine::_do_reset()；最顶层入口：Web 停止本局游戏或异常恢复。
	godot_js_spx_on_engine_reset__sig: 'v',
	godot_js_spx_on_engine_reset: function () {
		GodotGdspx.endContactSession("OnEngineReset");
	},

	godot_js_spx_on_reset_done__sig: 'vj',
	godot_js_spx_on_reset_done: function (code) {
		GodotGdspx.notifyRuntime("gdspx_on_runtime_reset", Number(code));
	},

	godot_js_spx_on_engine_pause__sig: 'vi',
	godot_js_spx_on_engine_pause: function (is_on) {
		GodotGdspx.dispatch("OnEnginePause", is_on);
	},

	godot_js_spx_on_scene_sprite_instantiated__sig: 'vji',
	godot_js_spx_on_scene_sprite_instantiated: function (obj, type_name) {
		GodotGdspx.dispatch(
			"OnSceneSpriteInstantiated",
			GodotGdspx.splitInt64(obj),
			GodotRuntime.parseString(type_name)
		);
	},

	godot_js_spx_on_runtime_panic__sig: 'vi',
	godot_js_spx_on_runtime_panic: function (msg) {
		GodotGdspx.notifyRuntime("gdspx_on_runtime_panic", GodotRuntime.parseString(msg));
	},

	godot_js_spx_on_runtime_exit__sig: 'vj',
	godot_js_spx_on_runtime_exit: function (code) {
		GodotGdspx.notifyRuntime("gdspx_on_runtime_exit", Number(code));
	},

	godot_js_spx_on_sprite_ready__sig: 'vj',
	godot_js_spx_on_sprite_ready: function (obj) {
		GodotGdspx.dispatch("OnSpriteReady", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_updated__sig: 'vf',
	godot_js_spx_on_sprite_updated: function (delta) {
		GodotGdspx.dispatch("OnSpriteUpdated", delta);
	},

	godot_js_spx_on_sprite_fixed_updated__sig: 'vf',
	godot_js_spx_on_sprite_fixed_updated: function (delta) {
		GodotGdspx.dispatch("OnSpriteFixedUpdated", delta);
	},

	godot_js_spx_on_sprite_destroyed__sig: 'vj',
	godot_js_spx_on_sprite_destroyed: function (obj) {
		GodotGdspx.dispatch("OnSpriteDestroyed", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_frames_set_changed__sig: 'vj',
	godot_js_spx_on_sprite_frames_set_changed: function (obj) {
		GodotGdspx.dispatch("OnSpriteFramesSetChanged", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_animation_changed__sig: 'vj',
	godot_js_spx_on_sprite_animation_changed: function (obj) {
		GodotGdspx.dispatch("OnSpriteAnimationChanged", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_frame_changed__sig: 'vj',
	godot_js_spx_on_sprite_frame_changed: function (obj) {
		GodotGdspx.dispatch("OnSpriteFrameChanged", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_animation_looped__sig: 'vj',
	godot_js_spx_on_sprite_animation_looped: function (obj) {
		GodotGdspx.dispatch("OnSpriteAnimationLooped", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_animation_finished__sig: 'vj',
	godot_js_spx_on_sprite_animation_finished: function (obj) {
		GodotGdspx.dispatch("OnSpriteAnimationFinished", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_vfx_finished__sig: 'vj',
	godot_js_spx_on_sprite_vfx_finished: function (obj) {
		GodotGdspx.dispatch("OnSpriteVfxFinished", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_screen_exited__sig: 'vj',
	godot_js_spx_on_sprite_screen_exited: function (obj) {
		GodotGdspx.dispatch("OnSpriteScreenExited", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_sprite_screen_entered__sig: 'vj',
	godot_js_spx_on_sprite_screen_entered: function (obj) {
		GodotGdspx.dispatch("OnSpriteScreenEntered", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_mouse_pressed__sig: 'vj',
	godot_js_spx_on_mouse_pressed: function (keyid) {
		GodotGdspx.dispatch("OnMousePressed", GodotGdspx.splitInt64(keyid));
	},

	godot_js_spx_on_mouse_released__sig: 'vj',
	godot_js_spx_on_mouse_released: function (keyid) {
		GodotGdspx.dispatch("OnMouseReleased", GodotGdspx.splitInt64(keyid));
	},

	godot_js_spx_on_key_pressed__sig: 'vj',
	godot_js_spx_on_key_pressed: function (keyid) {
		GodotGdspx.dispatch("OnKeyPressed", GodotGdspx.splitInt64(keyid));
	},

	godot_js_spx_on_key_released__sig: 'vj',
	godot_js_spx_on_key_released: function (keyid) {
		GodotGdspx.dispatch("OnKeyReleased", GodotGdspx.splitInt64(keyid));
	},

	godot_js_spx_on_action_pressed__sig: 'vi',
	godot_js_spx_on_action_pressed: function (action_name) {
		GodotGdspx.dispatch("OnActionPressed", GodotRuntime.parseString(action_name));
	},

	godot_js_spx_on_action_just_pressed__sig: 'vi',
	godot_js_spx_on_action_just_pressed: function (action_name) {
		GodotGdspx.dispatch("OnActionJustPressed", GodotRuntime.parseString(action_name));
	},

	godot_js_spx_on_action_just_released__sig: 'vi',
	godot_js_spx_on_action_just_released: function (action_name) {
		GodotGdspx.dispatch("OnActionJustReleased", GodotRuntime.parseString(action_name));
	},

	godot_js_spx_on_axis_changed__sig: 'vif',
	godot_js_spx_on_axis_changed: function (action_name, value) {
		GodotGdspx.dispatch("OnAxisChanged", GodotRuntime.parseString(action_name), value);
	},

	godot_js_spx_on_collision_enter__sig: 'vjj',
	godot_js_spx_on_collision_enter: function (self_id, other_id) {
		GodotGdspx.queueContact(1, self_id, other_id);
	},

	godot_js_spx_on_collision_stay__sig: 'vjj',
	godot_js_spx_on_collision_stay: function (self_id, other_id) {
		GodotGdspx.queueContact(2, self_id, other_id);
	},

	godot_js_spx_on_collision_exit__sig: 'vjj',
	godot_js_spx_on_collision_exit: function (self_id, other_id) {
		GodotGdspx.queueContact(3, self_id, other_id);
	},

	godot_js_spx_on_trigger_enter__sig: 'vjj',
	godot_js_spx_on_trigger_enter: function (self_id, other_id) {
		GodotGdspx.queueContact(4, self_id, other_id);
	},

	godot_js_spx_on_trigger_stay__sig: 'vjj',
	godot_js_spx_on_trigger_stay: function (self_id, other_id) {
		GodotGdspx.queueContact(5, self_id, other_id);
	},

	godot_js_spx_on_trigger_exit__sig: 'vjj',
	godot_js_spx_on_trigger_exit: function (self_id, other_id) {
		GodotGdspx.queueContact(6, self_id, other_id);
	},

	godot_js_spx_on_ui_ready__sig: 'vj',
	godot_js_spx_on_ui_ready: function (obj) {
		GodotGdspx.dispatch("OnUiReady", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_ui_updated__sig: 'vj',
	godot_js_spx_on_ui_updated: function (obj) {
		GodotGdspx.dispatch("OnUiUpdated", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_ui_destroyed__sig: 'vj',
	godot_js_spx_on_ui_destroyed: function (obj) {
		GodotGdspx.dispatch("OnUiDestroyed", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_ui_pressed__sig: 'vj',
	godot_js_spx_on_ui_pressed: function (obj) {
		GodotGdspx.dispatch("OnUiPressed", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_ui_released__sig: 'vj',
	godot_js_spx_on_ui_released: function (obj) {
		GodotGdspx.dispatch("OnUiReleased", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_ui_hovered__sig: 'vj',
	godot_js_spx_on_ui_hovered: function (obj) {
		GodotGdspx.dispatch("OnUiHovered", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_ui_clicked__sig: 'vj',
	godot_js_spx_on_ui_clicked: function (obj) {
		GodotGdspx.dispatch("OnUiClicked", GodotGdspx.splitInt64(obj));
	},

	godot_js_spx_on_ui_toggle__sig: 'vji',
	godot_js_spx_on_ui_toggle: function (obj, is_on) {
		GodotGdspx.dispatch("OnUiToggle", GodotGdspx.splitInt64(obj), is_on);
	},

	godot_js_spx_on_ui_text_changed__sig: 'vji',
	godot_js_spx_on_ui_text_changed: function (obj, text) {
		GodotGdspx.dispatch("OnUiTextChanged", GodotGdspx.splitInt64(obj), GodotRuntime.parseString(text));
	},
};

autoAddDeps(GodotGdspx, '$GodotGdspx');
// Emscripten 在链接阶段读取下面的 mergeInto，而不是浏览器运行时的模块导入语法。
mergeInto(LibraryManager.library, GodotGdspx);
