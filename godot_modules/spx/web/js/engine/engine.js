/*
 * Godot Web 引擎启动器。
 *
 * 浏览器加载最终导出的 engine.js 后，本文件会提供全局 Engine 类。它负责：
 * 1. 下载并实例化 Godot WASM；
 * 2. 保存 Emscripten 返回的 Module；
 * 3. 初始化虚拟文件系统、Canvas 和 Godot 配置；
 * 4. 调用 Module.callMain()，真正进入 Godot C++ 主程序。
 *
 * 初学者阅读提示：
 * - class 定义类；static 表示直接通过类调用，不依赖具体实例。
 * - async 函数一定返回 Promise；await 表示等待 Promise 完成后再继续。
 * - Promise.then(fn) 注册成功后的后续操作，Promise.reject(error) 表示失败。
 * - this.xxx 表示“当前 Engine 实例”的字段或方法。
 * - const 不允许变量重新指向别的值；let 允许后续重新赋值。
 * - `文字 ${value}` 是模板字符串，会把 ${...} 的结果嵌入字符串。
 * - obj['name'] 与 obj.name 都是读取属性；使用字符串键可防止压缩器改名。
 * - (function () { ... }()) 是立即执行函数：定义后立刻运行，用来隐藏内部状态。
 */

/* 性能计时工具：只负责记录浏览器时间，不影响 Godot 的游戏时间。 */
class TimeProfiler {
	// 记录一个标签当前对应的高精度时间点。
	static mark(label) {
		if (!TimeProfiler['enabled']) return;
		try {
			TimeProfiler['marks'][label] = performance.now();
		} catch (e) {
			// 性能统计不能阻断游戏启动，因此这里忽略浏览器计时异常。
		}
	}
	// 计算两个已记录标签之间相隔的毫秒数。
	static measure(startLabel, endLabel) {
		if (!TimeProfiler['enabled']) return;
		const s = TimeProfiler['marks'][startLabel];
		const e = TimeProfiler['marks'][endLabel];
		if (s != null && e != null) {
			const cost = (e - s).toFixed(2);
			console.log(`[Perf] ${startLabel} → ${endLabel}: ${cost} ms`);
			return cost;
		}
		return null;
	}
	// 执行并等待 fn，把整个异步过程耗时打印出来；异常会记录后继续向外抛出。
	static async profile(label, fn) {
        if (!TimeProfiler['enabled']) return await fn();
        const start = performance.now();
        try {
            const result = await fn();
            const end = performance.now();
            console.log(`[Perf] ${label}: ${(end - start).toFixed(2)} ms`);
            return result;
        } catch (err) {
            const end = performance.now();
            console.warn(`[Perf] ${label} failed after ${(end - start).toFixed(2)} ms`);
            throw err;
        }
	}
	// 按传入顺序输出相邻标签间的耗时摘要。
	static summary(labels, note = '') {
		if (!TimeProfiler['enabled']) return;
		console.log(`==== Perf(${note}) Summary ====`);
		for (let i = 0; i + 1 < labels.length; i++) {
			const a = labels[i], b = labels[i + 1];
			TimeProfiler.measure(a, b);
		}
		console.log("======================");
	}
}

TimeProfiler['enabled'] = false; // 默认关闭，宿主可按日志级别开启。
TimeProfiler['marks'] = {};      // 普通对象，用“标签 -> 时间”形式保存记录。
// 再用字符串键暴露方法，防止 Closure Compiler 压缩时改掉外部调用名称。
TimeProfiler['mark'] = TimeProfiler.mark;
TimeProfiler['measure'] = TimeProfiler.measure;
TimeProfiler['profile'] = TimeProfiler.profile;
TimeProfiler['summary'] = TimeProfiler.summary;

// 同一份代码既可能运行在浏览器主线程，也可能运行在 Web Worker。
// 条件表达式“条件 ? A : B”在条件成立时取 A，否则取 B。
const globalScope = typeof window !== 'undefined' ? window :
                    typeof self !== 'undefined' ? self :
                    globalThis;

globalScope['profiler'] = TimeProfiler;

// 立即执行函数返回 SafeEngine，并把内部的 preloader、Promise 等状态封装起来。
const Engine = (function () {
	// 整个启动器共享一个预加载器，用它统一统计 WASM 和资源包的下载进度。
	const preloader = new Preloader();

	let loadPromise = null; // 正在进行或已经完成的 engine.wasm 下载任务。
	let loadPath = '';      // 当前引擎文件基础路径，不包含扩展名。
	let initPromise = null; // 当前初始化任务，用于避免同时重复初始化。

	/**
	 * Engine 的内部构造函数。
	 * 最近调用方：SafeEngine()。
	 * 最顶层入口：game.js 的 GameApp.initEngine() 执行 new Engine(gameConfig)。
	 * @param {EngineConfig} initConfig 宿主传入的启动配置。
	 */
	function Engine(initConfig) { // eslint-disable-line no-shadow
		// new InternalConfig 会填入默认值，并用 initConfig 覆盖用户指定项。
		this.config = new InternalConfig(initConfig);
		// rtenv 是 runtime environment 的缩写；初始化完成后指向 Godot Module。
		this.rtenv = null;
	}

	/**
	 * 下载指定基础路径下的引擎 WASM，例如 basePath="engine" 对应 engine.wasm。
	 * 最近调用方：使用 Godot 标准 startGame() 启动方式的宿主代码。
	 * 最顶层入口：浏览器页面启动流程；本项目的 GameApp 会自行 fetch engine.wasm，通常不走这里。
	 * @param {string} basePath 引擎文件基础路径。
	 * @param {number=} [size=0] 已知文件大小；不知道时可不传。
	 * @returns {Promise} 下载完成时解决的 Promise。
	 */
	Engine.load = function (basePath, size) {
		// 已经有下载任务时直接返回同一个 Promise，避免重复请求大体积 WASM。
		if (loadPromise == null) {
			loadPath = basePath;
			loadPromise = preloader.loadPromise(`${loadPath}.wasm`, size, true);
			requestAnimationFrame(preloader.animateProgress);
		}
		return loadPromise;
	};

	/**
	 * 清除对下载任务的引用，使之后可以重新加载引擎。
	 * 注意：这里只清空 loadPromise，不等同于立刻销毁正在运行的 Godot 实例。
	 */
	Engine.unload = function () {
		loadPromise = null;
	};

	// 对外构造器：每次构造都重新创建原型对象，隔离不同实例对原型的修改。
	function SafeEngine(initConfig) {
		// {...} 是对象字面量；这里集中定义所有实例方法，最后赋给 Engine.prototype。
		const proto = /** @lends Engine.prototype */ {
			/**
			 * 实例化 Godot WASM 并初始化虚拟文件系统，但还不调用 Godot main。
			 * 最近调用方：game.js 的 GameApp.initEngine() 调用 curGame.init()；start() 也会兜底调用。
			 * 最顶层入口：runner.html 的 window.initEngine()。
			 * @return {Promise} 初始化完成时解决的 Promise。
			 */
			init: function () {
				// 多次调用 init() 时，已有任务就直接复用，不重复创建 WASM 实例。
				if(initPromise != null){
					return Promise.resolve();
				}
				loadPath = this.config.executable;
				// 小游戏宿主把引擎文件放在 js/ 子目录。
				if(typeof miniEngine !== 'undefined' && miniEngine){
					loadPath = "js/"+loadPath;
				}
				// 普通 function 中的 this 会随调用方式变化，因此先保存当前 Engine 实例。
				const me = this;
				function doInit() {
					// 这里保留显式 new Promise 写法，以兼容旧版 Emscripten/Mono 的 Promise 行为。
					return new Promise(function (resolve, reject) {
						// getModuleConfig 产生 Emscripten 配置；Godot(...) 是构建产物提供的
						// 模块工厂，异步返回真正的 Godot Module。
						let gdmodule = me.config.getModuleConfig(loadPath, me.config.wasmEngine);
						Godot(gdmodule).then(function (module) {
							// 后续 gdspx.js 会通过全局 Module 查找 _gdspx_* 导出函数。
							globalScope['Module'] = module;
							const paths = me.config.persistentPaths;
							// WASM 崩溃钩子：记录错误并向页面派发自定义事件。
							module['onAbort'] = function (msg) {
								console.error("[Godot WASM Crashed] ", msg);
								window.dispatchEvent(new CustomEvent("godot-wasm-crash", {
									detail: msg
								}));
							};
							if (typeof miniEngine === 'undefined' || !miniEngine){
								// 普通浏览器模式初始化 /userfs 等持久化虚拟目录。
								module['initFS'](paths).then(function (err) {
									me.rtenv = module;
									if (me.config.unloadAfterInit) {
										Engine.unload();
									}
									resolve();
								});
							}else{
								// 小游戏环境的文件系统由宿主处理，直接保存 Module。
								me.rtenv = module;
								resolve();
							}
						});
					});
				}
				preloader.setProgressFunc(this.config.onProgress);
				initPromise = doInit();
				return initPromise;
			},

			/**
			 * 启动前预加载一个文件。字符串表示需要下载的 URL，ArrayBuffer 表示已有数据。
			 * @param {string|ArrayBuffer} file URL 或二进制内容。
			 * @param {string=} path 文件进入 Godot 虚拟文件系统后的路径。
			 * @returns {Promise} 文件准备好时解决的 Promise。
			 */
			preloadFile: function (file, path) {
				return preloader.preload(file, path, this.config.fileSizes[file]);
			},
			getPThread:function () {
				// 取得 Emscripten 的 PThread 管理对象，供 Worker 模式协调线程。
				return this.rtenv['getPThread']()
			},
			// 把引擎数据包写入 Godot 虚拟文件系统，并通知运行时哪些文件发生变化。
			// 最近调用方：GameApp.unpackEngineData()；最顶层入口：GameApp.InitEngine()。
			unpackEngineData:async function (dir, pckName, pckData) {
				let datas = []
				if ( pckName != "" ){
					datas.push({ "path": pckName, "data": pckData })
				}
				// 将项目数据写入指定虚拟目录。
				let files = []
				this.rtenv['deleteDirFS'](dir);
				for (let info of datas) {
					files.push(info.path)
					this.rtenv['copyToFS'](dir + "/" + info.path, info.data);
				}
				this.rtenv['updateGameDatas'](dir, files);
			},

			// 增量写入一组资源；for...of 用来依次遍历数组元素。
			// 最近调用方：GameApp.updateEngineFiles()；最顶层入口：GameApp.InitGame()。
			updateAssetsData: async function (dir, assetList) {
				try {
					const updatedPaths = [];

					for (const { name, data } of assetList) {
						const assetPath = `${dir}/${name}`;
						this.rtenv['copyToFS'](assetPath, data);
						updatedPaths.push(name);
					}

					this.rtenv['updateGameDatas'](dir, updatedPaths);

				} catch (e) {
					console.error(`[GodotFS] updateAssetsData failed: ${e.message}`);
				}
			},

			// 删除一组资源，并用删除过的相对路径通知 Godot 刷新资源状态。
			// 最近调用方：GameApp.updateEngineFiles()；最顶层入口：GameApp.InitGame()。
			deleteAssetsData: async function (dir, assetNames) {
				try {
					const deletedPaths = [];

					for (const name of assetNames) {
						const assetPath = `${dir}/${name}`;
						this.rtenv['deleteDirRecursive'](assetPath);
						deletedPaths.push(name);
					}

					this.rtenv['updateGameDatas'](dir, deletedPaths);

				} catch (e) {
					console.error(`[GodotFS] deleteAssetsData failed: ${e.message}`);
				}
			},

			// 让录制模块把已完成的视频作为文件下载到用户设备。
			downloadRecordedVideo: function (fileName) {
				if (this.rtenv == null) {
					throw new Error('Engine must be inited before downloading web recorder');
				}
				if (this.rtenv['downloadRecordedVideo']) {
					return this.rtenv['downloadRecordedVideo'](fileName);
				} else {
					return Promise.reject(new Error('Web recorder is not supported by this engine version. '
						+ 'Enable "Web Recorder" for your export preset and/or build your custom template with "web_recorder_enabled=yes".'));
				}
			},

			// 取得浏览器 Blob，供宿主自行预览、上传或保存录制结果。
			getRecordedVideoBlob: function () {
				if (this.rtenv == null) {
					throw new Error('Engine must be inited before getting web recorder');
				}
				if (this.rtenv['getRecordedVideoBlob']) {
					return this.rtenv['getRecordedVideoBlob']();
				} else {
					return Promise.reject(new Error('Web recorder is not supported by this engine version. '
						+ 'Enable "Web Recorder" for your export preset and/or build your custom template with "web_recorder_enabled=yes".'));
				}
			},

			/**
			 * 启动已经初始化的 Godot 实例。它会设置配置、复制预加载文件，并调用 main。
			 * 最近调用方：game.js 的 GameApp.initEngine() 调用 curGame.start()。
			 * 最顶层入口：runner.html 的 window.initEngine()。
			 * 调用完成只表示 Godot 主循环已经建立，不表示具体 .spx 游戏已经执行。
			 * @param {EngineConfig} override 本次启动临时覆盖的配置。
			 * @return {Promise} Godot main 已被调用时解决的 Promise。
			 */
			start: function (override) {
				this.config.update(override);
				const me = this;

				// then 中的代码只会在 init() 成功后执行。
				return me.init().then(function () {
					if (!me.rtenv) {
						return Promise.reject(new Error('The engine must be initialized before it can be started'));
					}

					me.rtenv['setRecorderCanvas'](me.config.canvas);

					initPromise = null
					let config = {};
					try {
						config = me.config.getGodotConfig(function () {
							me.rtenv = null;
						});
					} catch (e) {
						return Promise.reject(e);
					}
					// 把 Canvas、语言和退出回调等运行参数交给 Godot。
					me.rtenv['initConfig'](config);

					// 异步加载普通 GDExtension 动态库。
					if (me.config.gdextensionLibs.length > 0 && !me.rtenv['loadDynamicLibrary']) {
						return Promise.reject(new Error('GDExtension libraries are not supported by this engine version. '
							+ 'Enable "Extensions Support" for your export preset and/or build your custom template with "dlink_enabled=yes".'));
					}
					let libs = [];
					me.config.gdextensionLibs.forEach(function (lib) {
						// gdspx 已作为特殊扩展提前加载，这里跳过，避免重复加载。
						if(lib.startsWith('gdspx')) {
							console.log('Loading gdspx dynamic library:', lib);
							return
						}
						libs.push(me.rtenv['loadDynamicLibrary'](lib, { 'loadAsync': true }));
					});
					function executeMainLogic() {
						return new Promise(function (resolve, reject) {
							// 把预加载器暂存的资源复制进 Emscripten/Godot 虚拟文件系统。
							preloader.preloadedFiles.forEach(function (file) {
								me.rtenv['copyToFS'](file.path, file.buffer);
							});
							preloader.preloadedFiles.length = 0; // 清空数组，释放对大块资源数据的引用。
							// 这是启动分界点：此前只是准备 WASM，此处真正进入 Godot C++ main。
							// 最近调用方：Engine.start()；最顶层入口：GameApp.InitEngine()。
							// 后续 C++ 会执行 initialize_spx_module() 并安装 SPX 主循环回调。
							me.rtenv['callMain'](me.config.args);
							initPromise = null;
							me.installServiceWorker();
							resolve();
						});
					}
					return executeMainLogic();

				});
			},

			/**
			 * 常规的一步式启动入口：并行初始化引擎和预加载主 PCK，随后调用 start()。
			 * 最近调用方/最顶层入口：采用 Godot 标准 Web API 的外部宿主。
			 * 本项目的 GameApp 为了分别管理 engine.zip 与 game.zip，没有使用这个便捷入口。
			 * @param {EngineConfig} override 本次启动临时覆盖的配置。
			 * @return {Promise} 启动完成时解决的 Promise。
			 */
			startGame: function (override) {
				this.config.update(override);
				// 把主资源包路径转换成 Godot 命令行参数 --main-pack。
				const exe = this.config.executable;
				const pack = this.config.mainPack || `${exe}.pck`;
				this.config.args = ['--main-pack', pack].concat(this.config.args);
				// Promise.all 表示 init 和 PCK 预加载都完成后才进入 then。
				const me = this;
				return Promise.all([
					this.init(exe),
					this.preloadFile(pack, pack),
				]).then(function () {
					return me.start.apply(me);
				});
			},

			/**
			 * 在 Godot 虚拟文件系统的 path 位置创建文件。
			 * @param {string} path 目标路径。
			 * @param {ArrayBuffer} buffer 文件二进制内容。
			 */
			copyToFS: function (path, buffer) {
				if (this.rtenv == null) {
					throw new Error('Engine must be inited before copying files');
				}
				this.rtenv['copyToFS'](path, buffer);
			},

			// 把持久化目录中的文件复制给宿主适配器，例如浏览器或小游戏存储层。
            copyFSToAdapter: function (adapter) {
                if (this.rtenv == null) {
                    throw new Error('Engine must be inited before copying files');
                }
                const me = this;
                var promises = [];
                this.config.persistentPaths.forEach(function (path) {
                    promises.push(me.rtenv['copyToAdapter'](path, adapter));
                });
                return Promise.all(promises);
            },

			// 返回 Godot Web Audio 使用的 AudioContext。
			getAudioContext: function () {
				if (this.rtenv == null) {
					throw new Error('Engine must be inited before getting audio context');
				}
				return this.rtenv['getAudioContext']();
			},

			/**
			 * 请求当前 Godot 实例正常退出；如果引擎已崩溃或死循环，请求可能无法处理。
			 */
			requestQuit: function () {
				if (this.rtenv) {
					this.rtenv['request_quit']();
				}
			},

			/**
			 * 请求重置当前 Godot 实例，相当于重新开始一次运行会话。
			 */
			requestReset: function () {
				if (this.rtenv) {
					this.rtenv['request_reset']();
				}
			},

			/**
			 * 配置了路径时安装 PWA Service Worker；不支持或未配置时返回已完成 Promise。
			 * @returns {Promise} 浏览器的 Service Worker 注册任务。
			 */
			installServiceWorker: function () {
				if (this.config.serviceWorker && 'serviceWorker' in navigator) {
					try {
						return navigator.serviceWorker.register(this.config.serviceWorker);
					} catch (e) {
						return Promise.reject(e);
					}
				}
				return Promise.resolve();
			},
		};

		Engine.prototype = proto;
		// 用固定字符串键导出实例方法，避免 Closure Compiler 压缩后宿主找不到名称。
		Engine.prototype['init'] = Engine.prototype.init;
		Engine.prototype['preloadFile'] = Engine.prototype.preloadFile;
		Engine.prototype['getPThread'] = Engine.prototype.getPThread;
		Engine.prototype['unpackEngineData'] = Engine.prototype.unpackEngineData;
		Engine.prototype['updateAssetsData'] = Engine.prototype.updateAssetsData;
		Engine.prototype['deleteAssetsData'] = Engine.prototype.deleteAssetsData;
		Engine.prototype['downloadRecordedVideo'] = Engine.prototype.downloadRecordedVideo;
		Engine.prototype['getRecordedVideoBlob'] = Engine.prototype.getRecordedVideoBlob;
		Engine.prototype['start'] = Engine.prototype.start;
		Engine.prototype['startGame'] = Engine.prototype.startGame;
		Engine.prototype['copyToFS'] = Engine.prototype.copyToFS;
		Engine.prototype['copyFSToAdapter'] = Engine.prototype.copyFSToAdapter;
		Engine.prototype['getAudioContext'] = Engine.prototype.getAudioContext;
		Engine.prototype['requestQuit'] = Engine.prototype.requestQuit;
		Engine.prototype['requestReset'] = Engine.prototype.requestReset;
		Engine.prototype['installServiceWorker'] = Engine.prototype.installServiceWorker;
		// 同时允许通过实例调用静态 load/unload，保持 Godot Web 原有 API 形态。
		Engine.prototype['load'] = Engine.load;
		Engine.prototype['unload'] = Engine.unload;
		return new Engine(initConfig);
	}

	// 导出静态方法，同样使用固定字符串键保护名称。
	SafeEngine['load'] = Engine.load;
	SafeEngine['unload'] = Engine.unload;

	// 把 Godot 提供的浏览器能力检测函数挂到 Engine 上。
	SafeEngine['isWebGLAvailable'] = Features.isWebGLAvailable;
	SafeEngine['isFetchAvailable'] = Features.isFetchAvailable;
	SafeEngine['isSecureContext'] = Features.isSecureContext;
	SafeEngine['isCrossOriginIsolated'] = Features.isCrossOriginIsolated;
	SafeEngine['isSharedArrayBufferAvailable'] = Features.isSharedArrayBufferAvailable;
	SafeEngine['isAudioWorkletAvailable'] = Features.isAudioWorkletAvailable;
	SafeEngine['getMissingFeatures'] = Features.getMissingFeatures;

	return SafeEngine;
}());
if (typeof window !== 'undefined') {
	// 普通网页模式暴露 window.Engine；Worker 没有 window，所以不会执行这一段。
	window['Engine'] = Engine;
}
