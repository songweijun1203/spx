var Module = null

/**
 * 项目文件的增量同步元数据。null 表示上次同步可能只完成了一部分，
 * 下一次必须重新发送该路径。
 * @typedef {{ lastModified: number }} FileMeta - Timestamp in milliseconds since Unix epoch.
 * @typedef {{ [path: string]: FileMeta | null }} FilesMeta - Null entries need resync.
 */

/**
 * Web 运行器编排 Godot Engine、Go 逻辑 WASM、基础 engine.zip 和项目文件。
 * 对外的 Init/Start/Stop 操作通过 taskTail 串行，避免重启与资源更新交叉执行。
 */
class GameRunner {
    constructor(config) {
        config = config || {};
        this.config = config;
        this.editor = null;
        this.engine = null;
        this.packName = 'engine.zip';
        this.persistentPath = 'engine';
        this.logLevel = config.logLevel;
        this.useProfiler = this.logLevel == LOG_LEVEL_VERBOSE;
        this.canvas = config.gameCanvas;
        this.assetURLs = config.assetURLs;
        // 传给 engine.js 的 Godot WASM 配置；项目资源不放在这里。
        this.engineConfig = {
            executable: 'engine',
            unloadAfterInit: false,
            canvas: this.canvas,
            logLevel: this.logLevel,
            canvasResizePolicy: 2,
            onExit: (code) => {
                this.onGodotExit(code)
            },
        };
        this.recordingOnGameStart = config.recordingOnGameStart || false
        this.autoDownloadRecordedVideo = config.autoDownloadRecordedVideo || false
        // 所有可变生命周期操作都链接到该 Promise 尾部。
        this.taskTail = Promise.resolve();
        this.workerMode = EnginePackMode == "worker"
        this.minigameMode = EnginePackMode == "minigame"
        this.miniprogramMode = EnginePackMode == "miniprogram"
        this.normalMode = !this.workerMode && !this.minigameMode && !this.miniprogramMode
        this.logicWasmInstance = null
        this.go = null

        profiler.enabled = this.useProfiler;

        // Worker 模式用它把项目数据和自定义调用转发到 Godot PThread。
        this.workerMessageManager = new globalThis.WorkerMessageManager();

        // pendingStops 让已经排队但尚未执行的停止请求也能阻止新的 init/start。
        this.pendingStops = 0;
        // gameLifecycle 表示当前具体游戏局；底层 Engine 可以跨多局复用。
        this.gameLifecycle = null;
        this.logVerbose('Engine mode:', EnginePackMode)

        /** @type {FilesMeta} */
        this.projectFilesMeta = Object.create(null);
    }

    async InitEngine() {
        return this.enqueue(() => this.initEngine())
    }

    // InitGame 在 Engine 就绪后同步项目文件并编译脚本，尚不执行项目 main。
    async InitGame(files) {
        return this.enqueue(() => this.initGame(files))
    }

    async StartGame(options = {}) {
        // 支持 Promise 配置，仍在真正取得队列执行权后等待其归一化结果。
        const inputSession = options && typeof options.then === 'function'
            ? Promise.resolve(options).then((resolved) => this.normalizeStartInput(resolved))
            : this.normalizeStartInput(options)
        return this.enqueue(async () => this.startGame(await inputSession))
    }

    async ResetGame() {
        // Reset 不要求输出录制结果；Stop 则在退出前完成输入录制。
        return this.enqueueStop(false)
    }

    async StopGame(beforeStop = null) {
        if (beforeStop != null && typeof beforeStop !== 'function') {
            throw new TypeError('beforeStop must be a function')
        }
        return this.enqueueStop(true, beforeStop)
    }

    pause() {
        if (this.engine == null || this.engine.rtenv == null) return
        this.callRuntime('_gdspx_ext_pause')
    }

    isPaused() {
        if (this.engine == null || this.engine.rtenv == null) return false
        return !!this.callRuntime('_gdspx_ext_is_paused')
    }

    resume() {
        this.callRuntime('_gdspx_ext_resume')
    }

    stepNextFrame() {
        this.callRuntime('_gdspx_ext_next_frame')
    }

    startRecording() {
        Module.tryStartRecording()
    }

    async stopRecording() {
        return await Module.tryStopRecording()
    }

    getRecordedVideo() {
        return Module.getRecordedVideoBlob()
    }

    downloadRecordedVideo(fileName) {
        Module.downloadRecordedVideo(fileName)
    }

    getInputSessionStatus() {
        // 普通 Web 模式下转发到 ispx.wasm 注册的 syscall/js 函数。
        return this.callInputFunction('ispx_input_session_status')
    }

    async waitInputSessionCompleted(options = {}) {
        // 轮询只观察状态，不推进帧；回放由 Godot 主循环继续驱动。
        this.ensureInputSessionSupported()
        if (options == null) options = {}
        if (typeof options !== 'object' || Array.isArray(options)) {
            throw new TypeError('Wait options must be an object')
        }
        const pollIntervalMs = options.pollIntervalMs == null ? 50 : options.pollIntervalMs
        const timeoutMs = options.timeoutMs == null ? 0 : options.timeoutMs
        if (!Number.isFinite(pollIntervalMs) || pollIntervalMs < 0) {
            throw new RangeError('pollIntervalMs must be a finite, non-negative number')
        }
        if (!Number.isFinite(timeoutMs) || timeoutMs < 0) {
            throw new RangeError('timeoutMs must be a finite, non-negative number')
        }

        const startedAt = Date.now()
        while (true) {
            if (options.signal && options.signal.aborted) {
                throw options.signal.reason || new DOMException('Input replay wait aborted', 'AbortError')
            }

            const status = this.getInputSessionStatus()
            if (status.completed === true || status.phase === 'completed') {
                return status
            }
            if (status.phase === 'aborted') {
                throw new Error(status.error || 'Input replay aborted')
            }
            if (status.mode !== 'replay' && status.mode !== 'replaying') {
                throw new Error(`Input replay is not running (mode: ${status.mode})`)
            }
            if (timeoutMs > 0 && Date.now() - startedAt >= timeoutMs) {
                throw new Error(`Input replay did not complete within ${timeoutMs} ms`)
            }
            await new Promise((resolve) => setTimeout(resolve, pollIntervalMs))
        }
    }

    onRuntimeReset(code) {
        // C++ runtime reset 与 Godot 整体退出都可以结束当前具体游戏局。
        this.completeGameLifecycle(code)
    }

    onGodotExit(code) {
        this.completeGameLifecycle(code)
        this.engine = null
        if (this.config.handleGodotExit != null) {
            this.config.handleGodotExit(code);
        }
    }

    callWorkerFunction(name, ...args) {
        this.workerMessageManager.callWorkerFunction(name, ...args)
    }

    // enqueue 把生命周期操作串到 taskTail。失败只拒绝当前调用，并恢复一条
    // 可继续使用的队列尾，避免一个 rejected Promise 永久阻断后续操作。
    enqueue(task) {
        const result = this.taskTail.then(() => task());
        this.taskTail = result;
        result.catch(() => {
            if (this.taskTail === result) this.taskTail = Promise.resolve();
        });
        return result;
    }

    async enqueueStop(finishRecording, beforeStop = null) {
        // 在停止任务排到队首前就增加计数，使并发提交的 init/start 能看见停止意图。
        this.pendingStops++;
        try {
            return await this.enqueue(() => this.stopGame(finishRecording, beforeStop));
        } finally {
            this.pendingStops--;
        }
    }

    async initEngine() {
        // 本函数只启动可复用底层引擎；具体项目由 InitGame/StartGame 分阶段进入。
        if (this.pendingStops > 0) {
            this.logVerbose('Engine initialization skipped: stop requested');
            return;
        }
        if (this.engine) {
            this.logVerbose('Engine initialization skipped: engine already exists');
            return;
        }

        // Godot WASM、Go 逻辑 WASM 和基础资源包并行下载，缩短冷启动关键路径。
        const engineReady = profiler.profile('loadEngineWasm', () => this.loadEngineWasm());
        const logicReady = !this.workerMode && !this.minigameMode
            ? fetch(this.wasmURL('ispx.wasm')) : null;
        const packReady = fetch(this.assetURLs[this.packName]).then(response => response.arrayBuffer());
        // 在稍后 await 前先安装 rejection 观察者，避免浏览器报告未处理拒绝。
        if (logicReady) logicReady.catch(() => {});
        packReady.catch(() => {});
        await engineReady;
        if (this.pendingStops > 0) return;

        const args = ['--main-pack', `${this.persistentPath}/${this.packName}`];
        if (this.recordingOnGameStart) {
            args.push('--write-movie', `${this.persistentPath}/movie.avi`);
        }

        this.logVerbose('Starting engine:', args);
        this.onProgress(0.5);
        this.engine = new Engine(this.engineConfig);
        const engine = this.engine;
        // 先安装占位和 gdspx_* 包装，Go/Godot 任一侧提前查询都不会得到 undefined。
        this.bindRuntimeCallbacks();
        // 普通模式在 Godot init 前启动 Go 宿主；Worker 模式由 PThread 稍后完成。
        await profiler.profile('prepareRuntime', () => this.prepareRuntime(logicReady));
        this.onProgress(0.5);

        await profiler.profile('engine.init', () => engine.init());

        this.onProgress(0.6);

        // engine.zip 是 Godot/SPX 基础资源，不是用户项目资源。
        await profiler.profile('unpackEngineData', () => packReady.then(data =>
            engine.unpackEngineData(this.persistentPath, this.packName, data)));

        this.onProgress(0.7);

        await profiler.profile('initRuntime', () => this.initRuntime(engine));

        this.onProgress(0.8);

        // Engine.start 最终进入 Module.callMain，Godot 从此开始主循环。
        await profiler.profile('engine.start', () => engine.start({ args, canvas: this.canvas }));

        this.onProgress(1.0);
        this.logVerbose('Engine started');
    }

    async loadEngineWasm() {
        // 小游戏平台把 URL 交给适配层；浏览器模式预取 ArrayBuffer 给 Engine.init。
        const url = this.wasmURL('engine.wasm')
        if (this.minigameMode) {
            this.engineConfig.wasmEngine = url
        } else if (!this.engineConfig.wasmEngine) {
            this.engineConfig.wasmEngine = await (await fetch(url)).arrayBuffer();
        }
    }

    bindRuntimeCallbacks() {
        // Go webffi.Link 会覆盖占位入口；GdspxFuncs 则提供 Go -> Godot 的 JS ABI。
        window.go_wasm_init = function () { }
        window.gdspx_dispatch = function () { }
        const callbacks = new GdspxFuncs();
        const names = Object.getOwnPropertyNames(Object.getPrototypeOf(callbacks));
        for (const name of names) {
            if (name.startsWith('gdspx_') && typeof callbacks[name] === 'function') {
                globalThis[name] = callbacks[name].bind(callbacks);
            }
        }
    }

    async prepareRuntime(logicReady) {
        // 普通模式现在就实例化并运行 ispx.wasm 宿主；Worker 模式没有主线程 Go 实例。
        if (this.minigameMode) {
            GameGlobal.engine = this.engine;
            godotSdk.set_engine(this.engine);
        } else if (this.workerMode) {
            return;
        } else {
            await profiler.profile('loadLogicWasm', () => this.loadLogicWasm(logicReady));
            await profiler.profile('runLogicWasm', () => this.runLogicWasm());
        }
        self.initExtensionWasm = function () { }
    }

    async initRuntime(engine) {
        // Godot WASM 已实例化但尚未启动主循环，在这里安装平台相关补充绑定。
        if (this.workerMode) {
            this.workerMessageManager.bindMainThreadCallbacks(engine)
        }
        if (this.minigameMode) {
            await this.loadLogicWasm()
        }
    }

    async initGame(files) {
        // 先写入 Godot 虚拟文件系统，再只把脚本/JSON 交给 Go 构建器。
        await profiler.profile('updateEngineFiles', () => this.updateEngineFiles(files));
        await profiler.profile('buildGame', () => this.buildGame(files));
    }

    updateEngineFiles(files) {
        // lastModified 相同的路径不重复跨 JS/WASM 复制；缺失路径显式从引擎删除。
        /** @type {Array<{ name: string, data: Uint8Array }>} */
        const updatedFiles = [];
        const previousMeta = this.projectFilesMeta;
        /** @type {FilesMeta} */
        const nextMeta = Object.create(null);
        for (const [path, { lastModified, content }] of Object.entries(files)) {
            // ZIP 可能包含目录条目；目录不能作为普通文件写入引擎 FS。
            if (path.endsWith('/')) {
                continue;
            }
            nextMeta[path] = { lastModified };
            const saved = previousMeta[path];
            if (saved != null && saved.lastModified === lastModified) {
                continue;
            }
            updatedFiles.push({ name: path, data: new Uint8Array(content) });
        }
        const removedPaths = Object.keys(previousMeta).filter(path => nextMeta[path] == null);

        try {
            this.engine.updateAssetsData(this.persistentPath, updatedFiles);
            this.engine.deleteAssetsData(this.persistentPath, removedPaths);
        } catch (error) {
            // 两个调用之间失败可能留下部分更新；把所有已知路径标为下次强制重发。
            const paths = Object.keys({ ...previousMeta, ...nextMeta });
            this.projectFilesMeta = Object.fromEntries(paths.map(path => [path, null]));
            throw error;
        }
        this.projectFilesMeta = nextMeta;
    }

    buildGame(files) {
        if (this.pendingStops > 0) {
            this.logVerbose('Game build skipped: stop requested');
            return;
        }
        // 图片和音频已经进入 Godot FS；构建器只需要 .spx 和 JSON。
        /** @type {{ [path: string]: Uint8Array }} */
        const nonAssetFiles = {};
        for (const [path, file] of Object.entries(files)) {
            if (path.endsWith(".spx") || path.endsWith('.json')) {
                nonAssetFiles[path] = new Uint8Array(file.content);
            }
        }
        if (!this.workerMode) {
            const result = window.ispx_build(nonAssetFiles);
            if (result instanceof Error) throw result;
        } else {
            // Worker 尚未加载 Go 逻辑 WASM，项目数据在 runProject 时一次发送。
            this.nonAssetFiles = nonAssetFiles;
        }
    }

    async startGame(inputSession) {
        // restart 让底层 runtime 为新一局复位；随后创建可由 reset/exit 完成的 Promise。
        if (this.pendingStops > 0) {
            this.logVerbose('Game start skipped: stop requested');
            return;
        }

        const engine = this.engine;
        profiler.mark('game.start.begin');
        await profiler.profile('restart', () => this.restart());
        const lifecycle = this.beginGameLifecycle(inputSession)
        try {
            await profiler.profile('runProject', () => this.runProject(engine, inputSession));
        } catch (error) {
            this.completeGameLifecycle(undefined, lifecycle)
            throw error
        }
        this.canvas.focus();
        profiler.mark('game.start.end');
        profiler.measure('game.start.begin', 'game.start.end');
    }

    async runProject(engine, inputSession) {
        if (this.minigameMode) {
            globalThis.FFI = self;
            this.runLogicWasm()
        }
        if (this.workerMode) {
            // Godot 主循环位于 PThread；项目数据到达后 Worker 才加载/启动 Go 逻辑。
            const threads = engine.getPThread()
            this.workerMessageManager.setPThreads(threads)
            this.workerMessageManager.callWorkerProjectDataUpdate(this.nonAssetFiles, this.assetURLs)
        } else {
            // 普通模式把 FFI 指向 window，随后 ispx_start 解释/运行具体项目 main。
            Module = engine.rtenv;
            globalThis.FFI = self;
            const result = window.ispx_start(inputSession);
            if (result instanceof Error) throw result;
            await this.waitInputSessionStarted(inputSession)
        }
    }

    async stopGame(finishRecording, beforeStop = null) {
        // 先冻结输入录制和宿主收尾工作，再请求 Go runtime 退出并等待 reset 回调。
        const lifecycle = this.gameLifecycle
        if (this.engine == null || lifecycle == null || lifecycle.completed) {
            this.logVerbose('No game is running')
            return { inputReplay: lifecycle?.inputReplay ?? null, stopped: false }
        }

        let inputReplay = null
        let stopError = null
        if (finishRecording && this.normalMode && lifecycle.inputSession?.mode === 'record') {
            try {
                inputReplay = this.finishInputRecording()
                lifecycle.inputReplay = inputReplay
            } catch (error) {
                stopError = error
            }
        }

        if (beforeStop != null && stopError == null) {
            try {
                await beforeStop({ inputReplay })
            } catch (error) {
                stopError = error
            }
        }

        const result = window.ispx_stop()
        if (result instanceof Error) {
            if (stopError == null) stopError = result
        } else {
            try {
                await lifecycle.exit
            } catch (error) {
                if (stopError == null) stopError = error
            }
        }

        if (this.recordingOnGameStart && this.autoDownloadRecordedVideo) {
            const fileName = `spx_${new Date().getTime()}.webm`;
            this.downloadRecordedVideo(fileName)
        }
        if (stopError != null) throw stopError
        return { inputReplay, stopped: true }
    }

    beginGameLifecycle(inputSession) {
        // 每局只有一个退出 Promise；底层 Engine 本身并不随正常 Stop 被销毁。
        if (this.gameLifecycle != null && !this.gameLifecycle.completed) {
            throw new Error('Game is already running')
        }
        let resolveExit
        const exit = new Promise((resolve) => {
            resolveExit = resolve
        })
        const lifecycle = {
            completed: false,
            inputSession,
            inputReplay: null,
            exit,
            resolveExit,
        }
        this.gameLifecycle = lifecycle
        return lifecycle
    }

    // 最近调用方：Godot 退出、runtime reset 或启动失败处理；唤醒 stopGame() 的等待。
    completeGameLifecycle(code, lifecycle = this.gameLifecycle) {
        if (lifecycle == null || lifecycle.completed) return false
        lifecycle.completed = true
        lifecycle.resolveExit(code)
        return true
    }

    restart() {
        // 请求已启动的 runtime 创建新一局，不重新实例化 Godot WASM。
        this.callRuntime('_gdspx_ext_request_restart')
    }

    callRuntime(name) {
        const fn = this.engine.rtenv[name]
        if (fn != null) return fn()
    }

    async loadLogicWasm(logicReady) {
        // 这里只实例化 ispx.wasm；runLogicWasm 才启动 Go main 和 syscall/js。
        const url = this.wasmURL('ispx.wasm')
        this.go = new Go();
        if (this.minigameMode) {
            const wasmResult = await WebAssembly.instantiate(url, this.go.importObject);
            // Adapt the mini-engine instance.
            this.logicWasmInstance = Object.create(WebAssembly.Instance.prototype);
            this.logicWasmInstance.exports = wasmResult.instance.exports;
            Object.defineProperty(this.logicWasmInstance, 'constructor', {
                value: WebAssembly.Instance,
                writable: false,
                enumerable: false,
                configurable: true
            });
        } else {
            const { instance } = await WebAssembly.instantiateStreaming(logicReady || fetch(url), this.go.importObject);
            this.logicWasmInstance = instance;
        }
    }

    runLogicWasm() {
        this.go.exit = (code) => {
            this.notifyExit(code);
        };
        // go.run 返回 Promise，但宿主需要它常驻；退出通过 go.exit/notifyExit 上报。
        this.go.run(this.logicWasmInstance);
    }

    wasmURL(name) {
        const url = this.assetURLs[name]
        return isWasmCompressed ? url + '.br' : url
    }

    notifyExit(code) {
        // 同时通知页面内回调、DOM 监听者和 iframe 父页面，兼容三种宿主接入方式。
        if (typeof window.onGoWasmExit === "function") {
            window.onGoWasmExit(code);
        }

        window.dispatchEvent(new CustomEvent("logicWasmExit", { detail: { code } }));

        if (window.parent !== window) {
            window.parent.postMessage({ type: "EngineCrash", code }, "*");
        }
    }

    normalizeStartInput(options) {
        // StartGame 的外层 options 与底层 Go 输入会话描述在这里分离。
        if (options == null) options = {}
        if (typeof options !== 'object' || Array.isArray(options)) {
            throw new TypeError('StartGame options must be an object')
        }
        return this.normalizeInputSession(options.input)
    }

    normalizeInputSession(input) {
        if (input == null) return null
        this.ensureInputSessionSupported()
        if (typeof input !== 'object' || Array.isArray(input)) {
            throw new TypeError('Input session must be an object')
        }
        if (input.mode === 'record') {
            // 为直接实例化 GameRunner 的宿主保留默认 FPS；runner 门面通常会先补齐。
            const fps = input.fps == null ? 30 : input.fps
            if (!Number.isFinite(fps) || fps <= 0) {
                throw new RangeError('Input recording FPS must be a positive finite number')
            }
            return { mode: 'record', fps, captureKey: this.normalizeCaptureKey(input.captureKey) }
        }
        if (input.mode === 'replay') {
            if (input.data == null) {
                throw new TypeError('Input replay data is required')
            }
            const objectTag = Object.prototype.toString.call(input.data)
            let data
            if (ArrayBuffer.isView(input.data)) {
                // 切片复制隔离宿主后续修改，并只保留视图覆盖的字节范围。
                data = new Uint8Array(input.data.buffer, input.data.byteOffset, input.data.byteLength).slice()
            } else if (objectTag === '[object ArrayBuffer]') {
                data = input.data.slice(0)
            } else if (typeof input.data === 'string') {
                data = input.data
            } else {
                throw new TypeError('Input replay data must be a string, ArrayBuffer, or ArrayBuffer view')
            }
            return { mode: 'replay', data, captureKey: this.normalizeCaptureKey(input.captureKey) }
        }
        throw new Error(`Unsupported input session mode: ${input.mode}`)
    }

    normalizeCaptureKey(value) {
        if (value == null) return null
        if (typeof value !== 'string' || value.length === 0) {
            throw new TypeError('Input session captureKey must be a non-empty string')
        }
        return value
    }

    ensureInputSessionSupported() {
        if (!this.normalMode) {
            throw new Error('Input recording and replay require normal Web mode')
        }
    }

    callInputFunction(name, ...args) {
        // Go API 可能以 Error 返回值表达同步失败，统一转换为 JS throw。
        this.ensureInputSessionSupported()
        const fn = window[name]
        if (typeof fn !== 'function') {
            throw new Error(`${name} is unavailable`)
        }
        const result = fn(...args)
        if (result instanceof Error) throw result
        return result
    }

    finishInputRecording() {
        return this.callInputFunction('ispx_input_recording_finish')
    }

    async waitInputSessionStarted(input, timeoutMs = 30000) {
        // ispx_start 返回只表示启动已提交；等待 Go Game 真正取得输入会话所有权。
        if (input == null) return
        const startedAt = Date.now()
        while (true) {
            const status = this.getInputSessionStatus()
            if (status.phase === 'running' || status.phase === 'finishing' || status.phase === 'completed') {
                return status
            }
            if (status.phase === 'aborted') {
                throw new Error(status.error || 'Input session startup aborted')
            }
            if (status.mode === 'idle') {
                throw new Error('No input session is attached to the game')
            }
            if (Date.now() - startedAt >= timeoutMs) {
                throw new Error(`Input session did not start within ${timeoutMs} ms`)
            }
            await new Promise((resolve) => setTimeout(resolve, 10))
        }
    }

    onProgress(value) {
        if (this.config.onProgress != null) {
            this.config.onProgress(value);
        }
    }

    logVerbose(...args) {
        if (this.logLevel == LOG_LEVEL_VERBOSE) {
            console.log(...args);
        }
    }
}

globalThis.GameRunner = GameRunner;
