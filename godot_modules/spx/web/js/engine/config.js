/*
 * Godot Web 引擎配置。
 *
 * 宿主页面会用普通 JavaScript 对象传入选项，例如：
 *     const config = { executable: 'engine', unloadAfterInit: false };
 *     const engine = new Engine(config);
 *
 * InternalConfig 会先提供默认值，再用用户传入的同名属性覆盖默认值。随后它分别
 * 生成两种配置：
 * - getModuleConfig()：交给 Emscripten，用于实例化 WASM、定位附属文件；
 * - getGodotConfig()：交给 Godot，用于 Canvas、语言、输入和退出回调。
 *
 * 初学者阅读提示：
 * - { key: value } 是对象字面量，作用类似 Go 的结构体实例加动态字段。
 * - null 表示“明确没有值”，undefined 通常表示“没有提供这个属性”。
 * - true/false 是布尔值；[] 是数组；function (...) { ... } 是函数值。
 * - prototype 上的方法会被该构造函数创建的所有实例共享。
 * - @param、@type 等是 JSDoc 类型说明，只是注释，不会在运行时执行。
 *
 * @typedef {Object} EngineConfig
 */
const EngineConfig = {}; // eslint-disable-line no-unused-vars
// 日志级别数值越大，允许输出的内容越少。
const LOG_LEVEL_VERBOSE = 0
const LOG_LEVEL_LOG = 1
const LOG_LEVEL_WARNING = 2
const LOG_LEVEL_ERROR = 3
const LOG_LEVEL_NONE = 4

let engineLogLevel = LOG_LEVEL_VERBOSE
/**
 * 把用户配置与默认值合并成内部配置对象。
 * @param {EngineConfig} initConfig 用户传入的原始配置。
 */
const InternalConfig = function (initConfig) { // eslint-disable-line no-unused-vars
	// cfg 是默认配置对象，后面会成为 Config.prototype。
	const cfg = /** @lends {InternalConfig.prototype} */ {
		/**
		 * WASM 实例化后是否丢弃之前缓存的下载 Promise，以便释放下载数据引用。
		 * @type {boolean}
		 */
		unloadAfterInit: true,
		/**
		 * Godot 用于渲染画面的 HTML Canvas；未指定时会查找页面第一个 canvas。
		 * @type {?HTMLCanvasElement}
		 */
		canvas: null,
		/**
		 * 引擎文件基础名，不含扩展名。例如 engine 对应 engine.wasm、engine.js。
		 * @type {string}
		 */
		executable: '',
		/**
		 * 主资源包路径；为 null 时 startGame() 默认使用 `${executable}.pck`。
		 * @type {?string}
		 */
		mainPack: null,
		/**
		 * Godot 本地化语言，例如 zh_CN；为 null 时读取浏览器语言。
		 * @type {?string}
		 */
		locale: null,
		/**
		 * Canvas 尺寸策略：0=宿主完全控制；1=启动和窗口变化时由 Godot 调整；
		 * 2=始终适配整个浏览器窗口。
		 * @type {number}
		 */
		canvasResizePolicy: 2,
		/**
		 * 传给 Godot main 的命令行参数；startGame() 会自动在前面加入 --main-pack。
		 * @type {Array<string>}
		 */
		args: [],
		/**
		 * 启动时是否让 Canvas 自动获得焦点，以便立即接收键盘事件。
		 * @type {boolean}
		 */
		focusCanvas: true,
		/**
		 * 是否启用移动端实验性虚拟键盘支持。
		 * @type {boolean}
		 */
		experimentalVK: false,
		/**
		 * 需要注册的 PWA Service Worker 脚本路径；空字符串表示不注册。
		 * @type {string}
		 */
		serviceWorker: '',
		/**
		 * 需要持久化同步的 Godot 虚拟目录。
		 * @type {Array.<string>}
		 */
		persistentPaths: ['/userfs'],
		/**
		 * 拖入文件是否写入持久化存储。
		 * @type {boolean}
		 */
		persistentDrops: false,
		/**
		 * 需要额外加载的 GDExtension 动态库路径。
		 * @type {Array.<string>}
		 */
		gdextensionLibs: [],
		/**
		 * 已知资源大小表，供下载进度在请求开始前得到准确 total。
		 * @type {Array.<string>}
		 */
		fileSizes: [],
		// 已下载的 engine.wasm 二进制数据；本项目可由宿主预先下载后直接传入。
		wasmEngine: null,
		/**
		 * Godot 调用 OS.execute 时交给宿主处理的回调。
		 * @type {?function(string, Array.<string>)}
		 */
		onExecute: null,
		/**
		 * Godot 正常退出时的回调，参数是退出码；崩溃或死循环时不保证触发。
		 * @type {?function(number)}
		 */
		onExit: null,
		/**
		 * 下载进度回调 (current, total)。total=0 表示服务器没有提供可靠总大小。
		 * 预加载器每个动画帧最多调用一次，宿主无需再套 requestAnimationFrame。
		 * @type {?function(number, number)}
		 */
		onProgress: null,
		/**
		 * Godot 标准输出处理函数。arguments 是 JavaScript 自动提供的全部实参数组；
		 * console.log.apply(console, ...) 相当于把这些参数逐个传给 console.log。
		 * @type {?function(...*)}
		 */
		onPrint: function () {
			if (engineLogLevel > LOG_LEVEL_LOG) {
				return
			}
			console.log.apply(console, Array.from(arguments)); // eslint-disable-line no-console
		},
		/**
		 * Godot 标准错误输出处理函数；超过允许日志级别时直接 return，不打印。
		 * @type {?function(...*)}
		 */
		onPrintError: function (var_args) {
			if (engineLogLevel > LOG_LEVEL_ERROR) {
				return
			}
			console.error.apply(console, Array.from(arguments)); // eslint-disable-line no-console
		},
	};

	/**
	 * @ignore
	 * @struct
	 * @constructor
	 * @param {EngineConfig} opts
	 */
	function Config(opts) {
		this.update(opts);
	}

	Config.prototype = cfg;

	/**
	 * @ignore
	 * @param {EngineConfig} opts
	 */
	Config.prototype.update = function (opts) {
		// 未传 opts 时用空对象；parse() 会保留当前值，而不是把它重置成 undefined。
		const config = opts || {};
		// parse 是本地小函数：配置对象没有 key 时返回传入的默认值，否则返回用户值。
		// 显式传默认值还能避免 Closure Compiler 压缩内部属性名造成错误。
		function parse(key, def) {
			if (typeof (config[key]) === 'undefined') {
				return def;
			}
			return config[key];
		}
		// 下面一组属性影响 Emscripten Module。
		this.unloadAfterInit = parse('unloadAfterInit', this.unloadAfterInit);
		this.onPrintError = parse('onPrintError', this.onPrintError);
		this.onPrint = parse('onPrint', this.onPrint);
		this.onProgress = parse('onProgress', this.onProgress);

		// 下面一组属性影响 Godot 本身。
		this.canvas = parse('canvas', this.canvas);
		this.executable = parse('executable', this.executable);
		this.mainPack = parse('mainPack', this.mainPack);
		this.locale = parse('locale', this.locale);
		this.canvasResizePolicy = parse('canvasResizePolicy', this.canvasResizePolicy);
		this.persistentPaths = parse('persistentPaths', this.persistentPaths);
		this.persistentDrops = parse('persistentDrops', this.persistentDrops);
		this.experimentalVK = parse('experimentalVK', this.experimentalVK);
		this.focusCanvas = parse('focusCanvas', this.focusCanvas);
		this.serviceWorker = parse('serviceWorker', this.serviceWorker);
		this.gdextensionLibs = parse('gdextensionLibs', this.gdextensionLibs);
		this.fileSizes = parse('fileSizes', this.fileSizes);
		this.args = parse('args', this.args);
		this.onExecute = parse('onExecute', this.onExecute);
		this.onExit = parse('onExit', this.onExit);

		// wasmEngine 是可选的预下载 ArrayBuffer。
		this.wasmEngine = parse('wasmEngine', this.wasmEngine);
		engineLogLevel = parse('logLevel', engineLogLevel);
	};

	/**
	 * @ignore
	 * 生成 Emscripten Module 的实例化配置，包括主 WASM、side.wasm 和文件定位规则。
	 * 最近调用方：engine.js 的 Engine.init()。
	 * 最顶层入口：GameApp.InitEngine() -> GameApp.initEngine() -> curGame.init()。
	 * @param {string} loadPath
	 * @param {(!ArrayBuffer|!ArrayBufferView)} buffer
	 */
	Config.prototype.getModuleConfig = function (loadPath, buffer) {
		// let 允许后续改值；这里保存宿主传入的 WASM 二进制数据。
		let curBuffer = buffer
		// 返回对象字面量，属性名是 Emscripten 识别的固定配置键。
		return {
			'print': this.onPrint,
			'printErr': this.onPrintError,
			'thisProgram': this.executable,
			// 允许 Godot main 退出时结束 Emscripten runtime。
			'noExitRuntime': false,
			// side.wasm 和额外 GDExtension 会在主模块之后加载。
			'dynamicLibraries': [`${loadPath}.side.wasm`].concat(this.gdextensionLibs),
			'instantiateWasm': function (imports, onSuccess) {
				// WebAssembly.instantiate 返回 Promise；成功后把实例和模块交回 Emscripten。
				WebAssembly.instantiate(curBuffer, imports).then((result) => {
					onSuccess(result['instance'], result['module']);
				});
				return {};
			},
			'locateFile': function (path) {
				// Godot 的默认文件名会被统一映射到本项目最终导出的 engine.* 文件名。
				if (!path.startsWith('godot.')) {
					return path;
				} else if (path.endsWith('.audio.worklet.js')) {
					return `${loadPath}.audio.worklet.js`;
				} else if (path.endsWith('.audio.position.worklet.js')) {
					return `${loadPath}.audio.position.worklet.js`;
				} else if (path.endsWith('.js')) {
					return `${loadPath}.js`;
				} else if (path.endsWith('.side.wasm')) {
					return `${loadPath}.side.wasm`;
				} else if (path.endsWith('.wasm')) {
					return `${loadPath}.wasm`;
				}
				return path;
			},
		};
	};

	/**
	 * @ignore
	 * 生成 Godot main 使用的 Canvas、语言、输入和退出配置。
	 * 最近调用方：engine.js 的 Engine.start()。
	 * 最顶层入口：GameApp.InitEngine() -> GameApp.initEngine() -> curGame.start()。
	 * @param {function()} cleanup
	 */
	Config.prototype.getGodotConfig = function (cleanup) {
		// 普通浏览器模式必须找到一个 HTMLCanvasElement；小游戏模式由宿主提供画面。
		if (typeof miniEngine === 'undefined' || !miniEngine){
			if (!(this.canvas instanceof HTMLCanvasElement)) {
				const nodes = document.getElementsByTagName('canvas');
				if (nodes.length && nodes[0] instanceof HTMLCanvasElement) {
					const first = nodes[0];
					this.canvas = /** @type {!HTMLCanvasElement} */ (first);
				}
				if (!this.canvas) {
					throw new Error('No canvas found in page');
				}
			}
		}
		// tabIndex < 0 表示不可通过键盘获得焦点；改为 0 后键盘输入才能进入 Canvas。
		if (this.canvas.tabIndex < 0) {
			this.canvas.tabIndex = 0;
		}

		// 没指定语言时，从浏览器首选语言推导；Godot 使用下划线格式。
		let locale = this.locale;
		if (!locale) {
			locale = navigator.languages ? navigator.languages[0] : navigator.language;
			locale = locale.split('.')[0];
		}
		locale = locale.replace('-', '_');
		const onExit = this.onExit;

		// 返回给 Module.initConfig() 的 Godot 运行配置对象。
		return {
			'canvas': this.canvas,
			'canvasResizePolicy': this.canvasResizePolicy,
			'locale': locale,
			'persistentDrops': this.persistentDrops,
			'virtualKeyboard': this.experimentalVK,
			'focusCanvas': this.focusCanvas,
			'onExecute': this.onExecute,
			'onExit': function (p_code) {
				// 退出前清理 rtenv 和临时资源，再转发给宿主自己的 onExit。
				cleanup();
				if (typeof (onExit) === 'function') {
					onExit(p_code);
				}
			},
		};
	};
	return new Config(initConfig);
};
