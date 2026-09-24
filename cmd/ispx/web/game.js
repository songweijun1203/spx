/*
 * Web 游戏总编排器。
 *
 * 本文件不实现 Godot 引擎本身，而是协调三部分：
 * 1. engine.js 提供的 Godot Engine 启动器；
 * 2. ispx.wasm 中的 Go/XGo 游戏运行时；
 * 3. engine.zip、game.zip 解压后得到的引擎资源和项目资源。
 *
 * 最近调用方：runner.html 暴露的 window.initEngine/initGame/startGame/stopGame。
 * 最顶层入口：浏览器加载页面后，由外层页面初始化引擎；用户点击 Start 后启动一局游戏。
 */

// Godot WASM 的 Emscripten Module。Engine.init() 成功后会指向实际运行时对象。
var Module = null

/**
 * @typedef {Object} FileMeta
 * @property {number} lastModified 文件最后修改时间，单位为 Unix 毫秒。
 */

/**
 * @typedef {Object} FileWithMeta
 * @property {number} lastModified 文件最后修改时间，单位为 Unix 毫秒。
 * @property {ArrayBuffer} content 文件的二进制内容。
 */

/**
 * @typedef {{ [path: string]: FileWithMeta }} Files 只包含文件，目录项应被忽略。
 * @typedef {{ [path: string]: FileMeta }} FilesMeta
 */

/**
 * 管理一个浏览器页面中的 Godot 引擎实例及多次 SPX 游戏会话。
 * 最近创建方：runner.html 的 window.initEngine()。
 * 最顶层入口：外层页面的 initializeRuntime()，或宿主直接调用 window.initEngine()。
 */
class GameApp {
    constructor(config) {
        config = config || {};
        this.config = config;
        this.editor = null;
        this.game = null;
        this.packName = 'engine.zip';
        this.persistentPath = 'engine';
        this.logLevel = config.logLevel;
        this.useProfiler = this.logLevel == LOG_LEVEL_VERBOSE;
        this.gameCanvas = config.gameCanvas;
        this.assetURLs = config.assetURLs;
        this.gameConfig = {
            "executable": "engine",
            'unloadAfterInit': false,
            'canvas': this.gameCanvas,
            'logLevel': this.logLevel,
            'canvasResizePolicy': 2,
            'onExit': (code) => {
                this.onGodotExit(code)
            },
        };
        this.recordingOnGameStart = config.recordingOnGameStart || false
        this.autoDownloadRecordedVideo = config.autoDownloadRecordedVideo || false
        this.logicPromise = Promise.resolve();
        this.workerMode = EnginePackMode == "worker"
        this.minigameMode = EnginePackMode == "minigame"
        this.miniprogramMode = EnginePackMode == "miniprogram"
        this.normalMode = !this.workerMode && !this.minigameMode && !this.miniprogramMode
        this.logicWasmInstance = null
        this.go = null

        profiler.enabled = this.useProfiler;

        this.workerMessageManager = new globalThis.WorkerMessageManager();

        this.stopGameTask = 0;
        this.gameLifecycle = null;
        this.logVerbose("EnginePackMode: ", EnginePackMode)

        /**
         * 上一次同步到引擎的项目文件元信息，用于判断文件是否发生变化。
         * @type FilesMeta
         */
        this.projectFilesMeta = {};
    }

    /**
     * 初始化并启动底层 Godot 引擎，但不编译、运行具体的 .spx 游戏。
     * 最近调用方：runner.html 的 window.initEngine()。
     * 最顶层入口：浏览器页面初始化流程。
     */
    async InitEngine() {
        return this.startTask(() => this.initEngine())
    }

    /**
     * 把当前项目文件写入 Godot 文件系统，并把 .spx/.json 交给 Go 解释器编译。
     * 最近调用方：runner.html 的 window.initGame()。
     * 最顶层入口：外层页面下载并解压 game.zip 后的 startProjectSession()。
     * 调用顺序：必须位于 InitEngine() 之后、StartGame() 之前。
     * @param {Files} files 项目文件表。
     * @returns {Promise<void>}
     */
    async InitGame(files) {
        return this.startTask(() => this.initGame(files))
    }

    /**
     * 启动已经编译好的本局游戏逻辑。
     * 最近调用方：runner.html 的 window.startGame()。
     * 最顶层入口：用户点击 Start，或宿主主动调用页面的 startGame()。
     */
    async StartGame(options = {}) {
        const inputSession = options && typeof options.then === 'function'
            ? Promise.resolve(options).then((resolved) => this.normalizeStartGameInput(resolved))
            : this.normalizeStartGameInput(options)
        return this.startTask(async () => this.startGame(await inputSession))
    }

    /**
     * 引擎崩溃后复用统一停止流程，清理当前游戏会话但不要求正常完成输入录制。
     * 最近调用方：runner.html 的 window.initEngine() 在已有 GameApp 时调用。
     * 最顶层入口：Godot/Go WASM 的退出或崩溃恢复流程。
     */
    async ResetGame() {
        this.stopGameTask++;
        return this.startTask(() => this.stopGame(false))
    }

    /**
     * 正常停止本局游戏；Web 下主要重置 SPX runtime，Godot WASM 通常继续存活。
     * 最近调用方：runner.html 的 window.stopGame()。
     * 最顶层入口：用户点击 Stop，或宿主主动停止游戏。
     */
    async StopGame(beforeStop = null) {
        if (beforeStop != null && typeof beforeStop !== 'function') {
            throw new TypeError('beforeStop must be a function')
        }
        this.stopGameTask++;
        return this.startTask(() => this.stopGame(true, beforeStop))
    }

    downloadRecordedVideo(fileName) {
        Module.downloadRecordedVideo(fileName)
    }

    getRecordedVideo() {
        return Module.getRecordedVideoBlob()
    }

    startRecording() {
        Module.tryStartRecording()
    }

    async stopRecording() {
        return await Module.tryStopRecording()
    }

    // 最近调用方：Engine 配置中的 onExit；最顶层来源：Godot main 正常退出。
    onGodotExit(code) {
        this.completeGameLifecycle(code)
        this.game = null
        if (this.config.handleGodotExit != null) {
            this.config.handleGodotExit(code);
        }

    }

    // 最近调用方：runner.html 的 runtime reset/崩溃处理；最顶层来源：一局游戏结束或异常重置。
    onRuntimeReset(code) {
        this.completeGameLifecycle(code)
    }

    /**
     * 请求 C++ SpxEngine 从 reset 状态恢复并重新执行各 Manager.on_start()。
     * 最近调用方：startGame()；最顶层入口：用户启动新一局游戏。
     * 首次启动时若 C++ runtime 尚未处于 reset 状态，该调用可以是空操作。
     */
    restart() {
        let funPtr = this.game.rtenv["_gdspx_ext_request_restart"]
        if(funPtr != null){
            funPtr()
        }
    }

    pause() {
        if (this.game == null || this.game.rtenv == null) return
        let funPtr = this.game.rtenv["_gdspx_ext_pause"]
        if(funPtr != null){
            funPtr()
        }
    }

    isPaused() {
        if (this.game == null || this.game.rtenv == null) return false
        const funPtr = this.game.rtenv["_gdspx_ext_is_paused"]
        return funPtr != null && !!funPtr()
    }

    resume() {
        let funPtr = this.game.rtenv["_gdspx_ext_resume"]
        if(funPtr != null){
            funPtr()
        }
    }

    stepNextFrame() {
        let funPtr = this.game.rtenv["_gdspx_ext_next_frame"]
        if(funPtr != null){
            funPtr()
        }
    }

    callWorkerFunction(funcName, ...args) {
        this.workerMessageManager.callWorkerFunction(funcName, ...args)
    }

    getInputSessionStatus() {
        return this.callInputReplayFunction('ispx_input_session_status')
    }

    async waitInputSessionCompleted(options = {}) {
        this.ensureInputReplaySupported()
        if (options == null) options = {}
        if (typeof options !== 'object' || Array.isArray(options)) {
            throw new TypeError('wait options must be an object')
        }
        const pollIntervalMs = options.pollIntervalMs == null ? 50 : options.pollIntervalMs
        const timeoutMs = options.timeoutMs == null ? 0 : options.timeoutMs
        if (!Number.isFinite(pollIntervalMs) || pollIntervalMs < 0) {
            throw new RangeError('pollIntervalMs must be a non-negative number')
        }
        if (!Number.isFinite(timeoutMs) || timeoutMs < 0) {
            throw new RangeError('timeoutMs must be a non-negative number')
        }

        const startedAt = Date.now()
        while (true) {
            if (options.signal && options.signal.aborted) {
                throw options.signal.reason || new DOMException('The operation was aborted', 'AbortError')
            }

            const status = this.getInputSessionStatus()
            const completed = status.completed === true || status.phase === 'completed'
            if (completed) {
                return status
            }
            if (status.phase === 'aborted') {
                throw new Error(status.error || 'input replay was aborted')
            }
            if (status.mode !== 'replay' && status.mode !== 'replaying') {
                throw new Error(`input replay is not running: ${status.mode}`)
            }
            if (timeoutMs > 0 && Date.now() - startedAt >= timeoutMs) {
                throw new Error(`timed out waiting for input replay after ${timeoutMs}ms`)
            }
            await new Promise((resolve) => setTimeout(resolve, pollIntervalMs))
        }
    }

    logVerbose(...args) {
        if (this.logLevel == LOG_LEVEL_VERBOSE) {
            console.log(...args);
        }
    }

    /**
     * 将初始化、启动、停止任务串行化，防止多个异步生命周期操作交叉执行。
     * 最近调用方：InitEngine/InitGame/StartGame/StopGame/ResetGame。
     * 最顶层入口：runner.html 暴露给宿主的同名操作。
     */
    startTask(taskFunc) {
        const originalPromise = this.logicPromise;
        const newPromise = this.logicPromise.then(() => taskFunc());
        this.logicPromise = newPromise;
        newPromise.catch((err) => {
            // 当前任务失败后恢复原任务链，避免后续操作永远被一个 rejected Promise 阻断。
            if (this.logicPromise === newPromise) this.logicPromise = originalPromise;
        })
        return this.logicPromise
    }

    normalizeStartGameInput(options) {
        if (options == null) options = {}
        if (typeof options !== 'object' || Array.isArray(options)) {
            throw new TypeError('StartGame options must be an object')
        }
        return this.normalizeInputSession(options.input)
    }

    /**
     * 完成一次底层 Web 引擎启动。
     * 最近调用方：InitEngine() 经 startTask() 调用。
     * 最顶层入口：runner.html 的 window.initEngine()。
     *
     * 普通模式顺序：下载 engine.wasm -> 启动 ispx.wasm 宿主 -> 实例化 Godot WASM
     * -> 写入 engine.zip -> Module.callMain() -> Godot 进入主循环。
     */
    async initEngine() {
        await profiler.profile('onRunPrepareEngineWasm', () => this.onRunPrepareEngineWasm());

        if (this.stopGameTask > 0) {
            this.logVerbose("stopGame is called before runing game");
            return;
        }

        let args = [
            '--main-pack', this.persistentPath + "/" + this.packName,
        ];
        if (this.recordingOnGameStart) {
            args.push('--write-movie', this.persistentPath + "/" + "movie.avi");
        }

        this.logVerbose("RunGame ", args);
        if (this.game) {
            this.logVerbose('A game is already running. Close it first');
            resolve();
            return;
        }

        this.onProgress(0.5);
        this.game = new Engine(this.gameConfig);
        let curGame = this.game;

        // 先提供两个占位函数，避免 Godot/Go 任一侧较早查询时得到 undefined。
        // Go 真正执行 webffi.Link() 后会用 syscall/js 覆盖它们。
        window.go_wasm_init = function () { }
        window.gdspx_dispatch = function () { }
        // 把 Go -> Godot 的全部 gdspx_* JS 包装方法挂到当前 globalThis。
        // 方法现在可以被 Go 找到，但要等 Engine.init() 设置 Module 后才能真正调用 C++。
        const spxfuncs = new GdspxFuncs();
        const methodNames = Object.getOwnPropertyNames(Object.getPrototypeOf(spxfuncs));
        methodNames.forEach(key => {
            if (key.startsWith('gdspx_') && typeof spxfuncs[key] === 'function') {
                globalThis[key] = spxfuncs[key].bind(spxfuncs);
            }
        });

        //[1] 加载并启动 ispx.wasm
        await profiler.profile('onRunBeforeInit', () => this.onRunBeforeInit());
        this.onProgress(0.5);

        //[2] 最近调用：GameApp.initEngine()；进入 engine.js 的 Engine.init()，实例化 Godot WASM。
        await profiler.profile('curGame.init',  () => curGame.init());

        this.onProgress(0.6);

        // engine.zip 是 Godot/SPX 的基础资源包，不是用户项目 game.zip。
        await profiler.profile('unpackData', () => this.unpackEngineData(curGame));

        this.onProgress(0.7);

        //[3]执行启动后的平台准备
        await profiler.profile('onRunAfterInit', () => this.onRunAfterInit(curGame));

        this.onProgress(0.8);

        //[4] 进入 engine.js 的 Engine.start()，其内部最终调用 Module.callMain()。
        await profiler.profile('curGame.start', () => curGame.start({ 'args': args, 'canvas': this.gameCanvas }));

        this.onProgress(1.0);
        this.logVerbose("==> engine start done");
    }

    /**
     * 同步项目资源与脚本编译，是“引擎已启动”到“本局可启动”之间的准备阶段。
     * 最近调用方：InitGame() 经 startTask() 调用。
     * 最顶层入口：外层页面的 startProjectSession()。
     * @param {Files} files 项目文件表。
     * @returns {Promise<void>}
     */
    async initGame(files) {
        await profiler.profile('updateEngineFiles', () => this.updateEngineFiles(files));
        await profiler.profile('buildGame', () => this.buildGame(files));
    }

    /**
     * 增量更新 Godot 虚拟文件系统中的项目文件，并通知 SpxResMgr 刷新资源。
     * 最近调用方：initGame()；最顶层入口：GameApp.InitGame()。
     * @param {Files} files 项目文件表。
     */
    updateEngineFiles(files) {
        /** @type Array<{ name: string, data: Uint8Array }> */
        const updatedFiles = [];
        const savedFilesMeta = this.projectFilesMeta;
        /** @type FilesMeta */
        const filesMeta = {};
        Object.entries(files).forEach(([path, { lastModified, content }]) => {
            // ZIP 解压器会把目录表示成以 / 结尾的条目；它不是文件，不能写入引擎文件系统。
            if (path.endsWith('/')) {
                return;
            }
            filesMeta[path] = { lastModified };
            const savedFileMeta = savedFilesMeta[path];
            if (savedFileMeta != null && savedFileMeta.lastModified === lastModified) {
                return; // 修改时间未变化，不重复复制文件内容。
            }
            updatedFiles.push({ name: path, data: new Uint8Array(content) });
        });
        this.game.updateAssetsData(this.persistentPath, updatedFiles)
        this.projectFilesMeta = filesMeta;

        /** @type Array<string> */
        const removedFilePaths = [];
        Object.entries(savedFilesMeta).forEach(([path, _]) => {
            if (filesMeta[path] == null) {
                removedFilePaths.push(path);
            }
        });
        this.game.deleteAssetsData(this.persistentPath, removedFilePaths);
    }

    /**
     * 提取 .spx/.json 并交给 ispx.wasm 编译；图片、音频等资源不参与脚本编译。
     * 最近调用方：initGame()；最顶层入口：GameApp.InitGame()。
     * 普通模式立即调用 window.ispx_build()；Worker 模式先缓存，稍后发送给 Worker。
     * @param {Files} files 项目文件表。
     */
    buildGame(files) {
        if (this.stopGameTask > 0) {
            this.logVerbose("stopGame is called before runing game");
            return;
        }
        /** @type {{ [path: string]: Uint8Array }} */
        const nonAssetFiles = {};
        Object.entries(files).forEach(([path, file]) => {
            if (path.endsWith(".spx") || path.endsWith('.json')) {
                nonAssetFiles[path] = new Uint8Array(file.content);
            }
        });
        if (!this.workerMode) {
            const res = window.ispx_build(nonAssetFiles);
            if (res instanceof Error) throw res;
        }else{
            this.nonAssetFiles = nonAssetFiles;
        }
    }

    /**
     * 建立本局生命周期并让 Go 解释器执行游戏 main。
     * 最近调用方：StartGame() 经 startTask() 调用。
     * 最顶层入口：用户点击 Start 或宿主调用 window.startGame()。
     */
    async startGame(inputSession) {
        if (this.stopGameTask > 0) {
            this.logVerbose("stopGame is called before runing game");
            return;
        }

        let curGame = this.game;
        profiler.mark('RunGame Start');
        await profiler.profile('restart', () => this.restart());
        const lifecycle = this.beginGameLifecycle(inputSession)
        try {
            await profiler.profile('onRunAfterStart', () => this.onRunAfterStart(curGame, inputSession));
        } catch (error) {
            this.completeGameLifecycle(undefined, lifecycle)
            throw error
        }
        this.gameCanvas.focus();
        profiler.mark('RunGame Done');
        profiler.measure('RunGame Start', 'RunGame Done');
    }

    /**
     * 请求 Go 游戏退出，并等待 C++ runtime reset 回调完成本局生命周期 Promise。
     * 最近调用方：StopGame()/ResetGame()；最顶层入口：停止、崩溃恢复或重新初始化。
     */
    async stopGame(finishInputRecording, beforeStop = null) {
        this.stopGameTask--
        const lifecycle = this.gameLifecycle
        if (this.game == null || lifecycle == null || lifecycle.completed) {
            this.logVerbose("No Game Is Running")
            return { inputReplay: lifecycle?.inputReplay ?? null, stopped: false }
        }

        let inputReplay = null
        let inputReplayError = null
        if (finishInputRecording && this.normalMode && lifecycle.inputSession?.mode === 'record') {
            try {
                inputReplay = this.finishInputRecording()
                lifecycle.inputReplay = inputReplay
            } catch (error) {
                inputReplayError = error
            }
        }

        let stopError = inputReplayError
        if (beforeStop != null && inputReplayError == null) {
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

        if(this.recordingOnGameStart && this.autoDownloadRecordedVideo){
            let fileName = `spx_${new Date().getTime()}.webm`;
            this.downloadRecordedVideo(fileName)
        }
        if (stopError != null) throw stopError
        return { inputReplay, stopped: true }
    }

    // 最近调用方：startGame()；为这一局创建一个可由 reset/exit 回调结束的 Promise。
    beginGameLifecycle(inputSession) {
        if (this.gameLifecycle != null && !this.gameLifecycle.completed) {
            throw new Error('A game is already running')
        }
        let resolveExit
        const lifecycle = {
            completed: false,
            inputSession,
            inputReplay: null,
            exit: new Promise((resolve) => {
                resolveExit = resolve
            }),
            resolveExit: null,
        }
        lifecycle.resolveExit = resolveExit
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

    onProgress(value) {
        if (this.config.onProgress != null) {
            this.config.onProgress(value);
        }
    }

    /**
     * 下载 engine.zip，并把它写到 Godot 虚拟文件系统的 engine/engine.zip。
     * 最近调用方：initEngine()；最顶层入口：GameApp.InitEngine()。
     */
    async unpackEngineData(game) {
        let packUrl = this.assetURLs[this.packName]
        let pckData = await (await fetch(packUrl)).arrayBuffer()
        await game.unpackEngineData(this.persistentPath, this.packName, pckData)
    }

    ensureInputReplaySupported() {
        if (!this.normalMode) {
            throw new Error('Input recording and replay are only supported in normal Web mode')
        }
    }

    callInputReplayFunction(funcName, ...args) {
        this.ensureInputReplaySupported()
        const fn = window[funcName]
        if (typeof fn !== 'function') {
            throw new Error(`${funcName} is not available`)
        }
        const result = fn(...args)
        if (result instanceof Error) throw result
        return result
    }

    normalizeInputSession(input) {
        if (input == null) return null
        this.ensureInputReplaySupported()
        if (typeof input !== 'object' || Array.isArray(input)) {
            throw new TypeError('input session must be an object')
        }
        if (input.mode === 'record') {
            // 为直接实例化 GameApp 的宿主保留底层默认值；runner 门面通常会在进入本层前补齐配置。
            const fps = input.fps == null ? 30 : input.fps
            if (!Number.isFinite(fps) || fps <= 0) {
                throw new RangeError('input recording FPS must be greater than zero')
            }
            return { mode: 'record', fps, captureKey: this.normalizeCaptureKey(input.captureKey) }
        }
        if (input.mode === 'replay') {
            if (input.data == null) {
                throw new TypeError('input replay data is required')
            }
            const objectTag = Object.prototype.toString.call(input.data)
            let data
            if (ArrayBuffer.isView(input.data)) {
                data = new Uint8Array(input.data.buffer, input.data.byteOffset, input.data.byteLength).slice()
            } else if (objectTag === '[object ArrayBuffer]') {
                data = input.data.slice(0)
            } else if (typeof input.data === 'string') {
                data = input.data
            } else {
                throw new TypeError('input replay data must be a string, ArrayBuffer, or Uint8Array')
            }
            return { mode: 'replay', data, captureKey: this.normalizeCaptureKey(input.captureKey) }
        }
        throw new Error(`Unsupported input session mode: ${input.mode}`)
    }

    normalizeCaptureKey(value) {
        if (value == null) return null
        if (typeof value !== 'string' || value.length === 0) {
            throw new TypeError('input session captureKey must be a non-empty key name string')
        }
        return value
    }

    finishInputRecording() {
        return this.callInputReplayFunction('ispx_input_recording_finish')
    }

    async waitInputSessionStarted(input, timeoutMs = 30000) {
        if (input == null) return
        const startedAt = Date.now()
        while (true) {
            const status = this.getInputSessionStatus()
            if (status.phase === 'running' || status.phase === 'finishing' || status.phase === 'completed') {
                return status
            }
            if (status.phase === 'aborted') {
                throw new Error(status.error || 'input session was aborted during startup')
            }
            if (status.mode === 'idle') {
                throw new Error('input session was not attached to the game')
            }
            if (Date.now() - startedAt >= timeoutMs) {
                throw new Error(`timed out waiting for input session startup after ${timeoutMs}ms`)
            }
            await new Promise((resolve) => setTimeout(resolve, 10))
        }
    }

    /**
     * 获取 Godot 的 engine.wasm 二进制；这里的“下载”就是浏览器通过 fetch 加载资源。
     * 最近调用方：initEngine()；最顶层入口：GameApp.InitEngine()。
     */
    async onRunPrepareEngineWasm() {
        let url = this.assetURLs["engine.wasm"]
        if (isWasmCompressed) {
            url += ".br"
        }

        if (this.minigameMode) {
            this.gameConfig.wasmEngine = url
        } else if (!this.gameConfig.wasmEngine) {
            this.gameConfig.wasmEngine = await (await fetch(url)).arrayBuffer();
        }
    }

    /**
     * 在 Godot WASM 实例化前准备 Go WASM。
     * 最近调用方：initEngine()；最顶层入口：GameApp.InitEngine()。
     * 普通模式在这里启动 ispx.wasm 宿主；Worker 模式由 Godot Worker 稍后加载。
     */
    async onRunBeforeInit() {
        if (this.minigameMode) {
            GameGlobal.engine = this.game;
            godotSdk.set_engine(this.game);
            self['initExtensionWasm'] = function () { }
        } else if (!this.workerMode) {
            await profiler.profile('loadLogicWasm', () => this.loadLogicWasm());
            await profiler.profile('runLogicWasm', () => this.runLogicWasm());
            self['initExtensionWasm'] = function () { }
        }
    }

    /**
     * Godot WASM 已实例化、尚未 callMain 时执行平台补充绑定。
     * 最近调用方：initEngine()；最顶层入口：GameApp.InitEngine()。
     */
    async onRunAfterInit(game) {
        if (this.workerMode) {
            this.workerMessageManager.bindMainThreadCallbacks(game)
        }
        if (this.minigameMode) {
            await this.loadLogicWasm()
        }
    }

    /**
     * 启动具体游戏逻辑。
     * 最近调用方：startGame()；最顶层入口：GameApp.StartGame()。
     * 普通模式把 FFI 指向 window 后调用 ispx_start；Worker 模式把项目数据发给 PThread Worker。
     */
    async onRunAfterStart(game, inputSession) {
        if (this.minigameMode) {
            globalThis['FFI'] = self;
            await this.runLogicWasm()
        }
        if (this.workerMode) {
            // Godot 主循环已经在 pthread Worker 中启动。此时把 Go 游戏需要的
            // .spx/.json 数据和资源 URL 发给 Worker；Worker 收到后才知道
            // ispx.wasm 的实际地址，并由 initExtensionWasm() 加载 Go WASM。
            let pthreads = game.getPThread()
            this.workerMessageManager.setPThreads(pthreads)
            this.workerMessageManager.callWorkerProjectDataUpdate(this.nonAssetFiles, this.assetURLs)
        } else {
            // 普通模式下 self 就是 window。library_godot_gdspx.js 将通过
            // FFI.gdspx_dispatch 找到 Go 在 webffi.Link() 中注册的事件入口。
            Module = game.rtenv;
            globalThis['FFI'] = self;
            const res = window.ispx_start(inputSession);
            if (res instanceof Error) throw res;
            await this.waitInputSessionStarted(inputSession)
        }
    }

    /**
     * 下载并实例化 ispx.wasm，但尚未开始执行 Go main()。
     * 最近调用方：onRunBeforeInit()/onRunAfterInit()。
     * 最顶层入口：GameApp.InitEngine()。
     */
    async loadLogicWasm() {
        let url = this.config.assetURLs["ispx.wasm"];
        if (isWasmCompressed) {
            url += ".br"
        }
        this.go = new Go();
        if (this.minigameMode) {
            // 小游戏平台使用宿主支持的 WebAssembly.instantiate()。
            const wasmResult = await WebAssembly.instantiate(url, this.go.importObject);
            // 构造与标准 WebAssembly.Instance 兼容的对象。
            this.logicWasmInstance = Object.create(WebAssembly.Instance.prototype);
            this.logicWasmInstance.exports = wasmResult.instance.exports;
            Object.defineProperty(this.logicWasmInstance, 'constructor', {
                value: WebAssembly.Instance,
                writable: false,
                enumerable: false,
                configurable: true
            });
        } else {
            const { instance } = await WebAssembly.instantiateStreaming(fetch(url), this.go.importObject);
            this.logicWasmInstance = instance;
        }
    }

    notifyExit(code) {
        if (typeof window.onGoWasmExit === "function") {
            window.onGoWasmExit(code);
        }

        window.dispatchEvent(new CustomEvent("logicWasmExit", { detail: { code } }));

        if (window.parent !== window) {
            window.parent.postMessage({ type: "EngineCrash", code }, "*");
        }
    }

    /**
     * 调用 Go.run()，启动 ispx.wasm 的 Go main、goroutine 和 syscall/js。
     * 最近调用方：onRunBeforeInit() 或小游戏 onRunAfterStart()。
     * 最顶层入口：GameApp.InitEngine()；这里只启动解释器宿主，不等于启动具体游戏。
     */
    async runLogicWasm() {
        this.go.exit = (code) => {
            this.notifyExit(code);
        };
        this.go.run(this.logicWasmInstance);
    }

}

globalThis.GameApp = GameApp;
