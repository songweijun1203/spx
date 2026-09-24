/*
 * Godot Web 资源预加载器。
 *
 * 它负责用浏览器 fetch 下载 engine.wasm、PCK/ZIP 等资源，记录下载进度，并把
 * 启动前下载好的文件暂存在 preloadedFiles。Engine.start() 随后把这些文件复制
 * 到 Godot 的虚拟文件系统。
 *
 * 初学者阅读提示：
 * - function (...) { ... } 是函数；bind(...) 会生成一个预先绑定部分参数的新函数。
 * - Promise 表示“未来才完成的结果”；then 处理成功，catch 处理失败。
 * - => 是箭头函数，这里只是更短的函数写法。
 * - const 保存不需要重新赋值的变量，let 保存之后还会重新赋值的变量。
 * - this.xxx 表示当前 Preloader 实例公开给外部使用的字段或方法。
 */

// 这里采用“构造函数”写法；执行 new Preloader() 会创建一份预加载状态。
const Preloader = /** @constructor */ function () { // eslint-disable-line no-unused-vars
	// 给 fetch 返回的 Response 包一层可跟踪进度的读取器。
	function getTrackedResponse(response, load_status) {
		// ReadableStream 是分块读取的；每读到一块就累加 loaded 字节数。
		function onloadprogress(reader, controller) {
			return reader.read().then(function (result) {
				if (load_status.done) {
					return Promise.resolve();
				}
				if (result.value) {
					controller.enqueue(result.value);
					load_status.loaded += result.value.length;
				}
				if (!result.done) {
					return onloadprogress(reader, controller);
				}
				load_status.done = true;
				return Promise.resolve();
			});
		}
		const reader = response.body.getReader();
		// new Response(new ReadableStream(...)) 重新构造响应，使后续代码仍可像普通
		// Response 一样调用 arrayBuffer()，同时我们能够观察每个数据块。
		return new Response(new ReadableStream({
			start: function (controller) {
				onloadprogress(reader, controller).then(function () {
					controller.close();
				});
			},
		}), { headers: response.headers });
	}

	// 发起一次实际下载，并把该文件的总大小、已下载大小和完成状态写入 tracker。
	// 最近调用方：retry() 包装后的 loadPromise()/preload()；最顶层入口：Engine.load()/preloadFile()。
	function loadFetch(file, tracker, fileSize, raw) {
		tracker[file] = {
			total: fileSize || 0,
			loaded: 0,
			done: false,
		};
		return fetch(file).then(function (response) {
			// HTTP 请求完成不等于成功；例如 404 也会得到 Response，因此要检查 ok。
			if (!response.ok) {
				return Promise.reject(new Error(`Failed loading file '${file}'`));
			}
			// 小游戏环境可能没有标准浏览器文件访问方式，改用宿主提供的文件系统 API。
			if (typeof miniEngine !== 'undefined' && miniEngine){
				return new Promise((resolve, reject) => {
					const fs = miniEngine['getFileSystemManager']();
					fs['readFile']({
						'filePath': file,
						'success': res => resolve(res['data']),
						'fail': reason => {
							reject(reason['errMsg']);
						}
					});
				});
			}else{
				const tr = getTrackedResponse(response, tracker[file]);
				// raw=true 时保留 Response，便于 WebAssembly 流式/定制加载；否则直接
				// 读取为 ArrayBuffer（二进制内存块）。
				if (raw) {
					return Promise.resolve(tr);
				}
				return tr.arrayBuffer();
			}
		});
	}

	// 下载失败后的重试包装。attempts 表示当前还允许尝试多少次。
	function retry(func, attempts = 1) {
		function onerror(err) {
			if (attempts <= 1) {
				return Promise.reject(err);
			}
			return new Promise(function (resolve, reject) {
				setTimeout(function () {
					retry(func, attempts - 1).then(resolve).catch(reject);
				}, 1000);
			});
		}
		return func().catch(onerror);
	}

	const DOWNLOAD_ATTEMPTS_MAX = 4; // 每个资源最多尝试四次。
	const loadingFiles = {};         // 文件路径 -> 当前下载状态。
	const lastProgress = { loaded: 0, total: 0 }; // 上一次通知出去的汇总进度。
	let progressFunc = null;         // 外部传入的进度回调，可以稍后替换。

	// 汇总所有正在下载文件的进度；每个动画帧最多通知一次，避免回调过于频繁。
	const animateProgress = function () {
		let loaded = 0;
		let total = 0;
		let totalIsValid = true;
		let progressIsFinal = true;

		Object.keys(loadingFiles).forEach(function (file) {
			const stat = loadingFiles[file];
			if (!stat.done) {
				progressIsFinal = false;
			}
			// 任意文件不知道总大小时，整体 total 也不能给出可信数值，约定返回 0。
			if (!totalIsValid || stat.total === 0) {
				totalIsValid = false;
				total = 0;
			} else {
				total += stat.total;
			}
			loaded += stat.loaded;
		});
		if (loaded !== lastProgress.loaded || total !== lastProgress.total) {
			lastProgress.loaded = loaded;
			lastProgress.total = total;
			if (typeof progressFunc === 'function') {
				progressFunc(loaded, total);
			}
		}
		if (!progressIsFinal) {
			requestAnimationFrame(animateProgress);
		}
	};

	// 把内部函数挂到 this 上，外部的 Engine 才能通过 preloader.xxx 调用。
	this.animateProgress = animateProgress;

	this.setProgressFunc = function (callback) {
		progressFunc = callback;
	};

	// 最近调用方：Engine.load() 或 preload()；最顶层入口：浏览器的 Godot 引擎/资源加载流程。
	this.loadPromise = function (file, fileSize, raw = false) {
		// bind 把 loadFetch 的前几个参数固定下来，retry 只需反复执行这个零参数函数。
		return retry(loadFetch.bind(null, file, loadingFiles, fileSize, raw), DOWNLOAD_ATTEMPTS_MAX);
	};

	// 已下载但尚未复制进 Godot 虚拟文件系统的资源。
	this.preloadedFiles = [];
	// 最近调用方：Engine.preloadFile()；最顶层入口：Godot 标准 Web startGame() 启动流程。
	this.preload = function (pathOrBuffer, destPath, fileSize) {
		let buffer = null;
		if (typeof pathOrBuffer === 'string') {
			// 传入字符串表示它是 URL/路径，需要先下载。
			const me = this;
			return this.loadPromise(pathOrBuffer, fileSize).then(function (buf) {
				me.preloadedFiles.push({
					path: destPath || pathOrBuffer,
					buffer: buf,
				});
				return Promise.resolve();
			});
		} else if (pathOrBuffer instanceof ArrayBuffer) {
			// 已经拿到二进制数据时，不再发起网络请求。
			buffer = new Uint8Array(pathOrBuffer);
		} else if (ArrayBuffer.isView(pathOrBuffer)) {
			buffer = new Uint8Array(pathOrBuffer.buffer);
		}
		if (buffer) {
			// 保存原始对象而不是这里创建的 Uint8Array；buffer 仅用于验证输入类型。
			this.preloadedFiles.push({
				path: destPath,
				buffer: pathOrBuffer,
			});
			return Promise.resolve();
		}
		return Promise.reject(new Error('Invalid object for preloading'));
	};
};
