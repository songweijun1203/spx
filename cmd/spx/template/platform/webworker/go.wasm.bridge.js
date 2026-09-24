/**
 * Worker 中的 Go WASM 桥接器。
 *
 * 这个类不创建 Worker，也不负责 Godot 的游戏帧循环；Worker 由 Emscripten 的
 * PROXY_TO_PTHREAD 创建。它只负责在已经存在的 Worker 中加载 ispx.wasm、启动 Go
 * 运行时，并把 Go 导出的 JavaScript 函数交给 Godot 的 JS 桥使用。
 */

class GoWasmBridge {
    constructor() {
        // 由 loadGoWasmModule() 创建实例；初始化完成后由 loader 保存到 self.goBridge。
        this.goInstance = null;
        this.goRuntime = null;
        this.isReady = false;
        this.pendingCalls = [];
        this.callCounter = 0;
        this.activeCalls = new Map();
        
        // Go WASM 的加载路径、调用超时和调试开关。
        this.config = {
            wasmPath: './main.wasm',
            timeout: 10000,
            enableDebug: false
        };

        // 绑定 this，确保这些函数作为 Promise 回调执行时仍指向当前桥接器实例。
        this.loadGoModule = this.loadGoModule.bind(this);
        this.callGoFunction = this.callGoFunction.bind(this);
        this.handleGoMessage = this.handleGoMessage.bind(this);
    }

    /**
     * 初始化 Go WASM。
     *
     * 调用来源：Worker 的 go.wasm.loader.js -> loadGoWasmModule()。
     * 调用结果：先加载 Go 运行时，再实例化 ispx.wasm，最后启动 go.run()。
     */
    async initialize(options = {}) {
        // 把 loader 传入的 wasmPath、timeout 等配置合并到默认配置。
        Object.assign(this.config, options);
        
        try {
            this.log('Initializing Go WASM module...');
            
            // 加载 go.wasm.exec.js 并创建 Go 运行时对象。
            await this.loadGoRuntime();

            // 下载、实例化并运行 ispx.wasm。
            await this.loadGoModule();
            
            this.log('Go WASM module initialization complete');
            return true;
            
        } catch (error) {
            this.error('Go WASM module initialization failed:', error);
            throw error;
        }
    }
    
    /**
     * 加载 Go 的 JavaScript 运行时并创建 Go 对象。
     *
     * 调用来源：initialize()。
     * 这里的 new Go() 来自 go.wasm.exec.js，不是创建 Worker。
     */
    loadGoRuntime() {
        return new Promise((resolve, reject) => {
            try {
                // Worker 中使用 importScripts() 引入 Go 运行时脚本。
                if (this.config.runtimePath !== undefined && this.config.runtimePath !== null && this.config.runtimePath !== '') {
                    importScripts(this.config.runtimePath);
                }

                // Go 对象负责提供 importObject，并在后面启动 Go WASM。
                this.goRuntime = new Go();
                this.log('Go runtime loaded successfully');
                resolve();
                
            } catch (error) {
                reject(new Error(`Failed to load Go runtime: ${error.message}`));
            }
        });
    }
    
    /**
     * 下载、实例化并启动 Go WASM。
     *
     * 调用来源：initialize()。
     * 关键步骤是 goRuntime.run(goInstance)，它启动 Go 的 main()、goroutine 和
     * syscall/js；它不是 Godot 的游戏帧循环。
     */
    async loadGoModule() {
        try {
            // 从 Module.gameAssetURLs['ispx.wasm'] 下载 Go WASM 二进制。
            const wasmBytes = await this.fetchWasm(this.config.wasmPath);

            // 使用 Go 运行时提供的 importObject 创建 Go WASM 实例。
            const wasmModule = await WebAssembly.instantiate(wasmBytes, this.goRuntime.importObject);
            this.goInstance = wasmModule.instance;

            // 接管 Go 运行时可能通过 self.postMessage 发出的内部通知。
            this.setupMessageHandling();

            // 等待 Go 导出的 JavaScript 函数出现，避免后续调用早于 Go 初始化完成。
            const readyPromise = new Promise((resolve, reject) => {
                const timeout = setTimeout(() => {
                    reject(new Error('Go module initialization timed out'));
                }, this.config.timeout || 15000);

                this._moduleReadyResolve = () => {
                    clearTimeout(timeout);
                    clearInterval(checkInterval);
                    resolve();
                };
                
                this._moduleReadyReject = (error) => {
                    clearTimeout(timeout);
                    clearInterval(checkInterval);
                    reject(error);
                };
                
                // 当前实现同时使用轮询作为兜底：Go 运行后，导出函数会挂到 Worker 的 self 上。
                const checkInterval = setInterval(() => {
                    const availableFunctions = this.getAvailableGoFunctions();
                    if (availableFunctions.length > 0) {
                        this.log('Detected Go functions available through polling:', availableFunctions);
                        this.isReady = true;
                        this._moduleReadyResolve();
                    }
                }, 100); // Check every 100ms
            });
            
            // 启动 Go WASM。Go 的 main() 会在这里开始运行，并注册 syscall/js 函数。
            this.goRuntime.run(this.goInstance).catch(error => {
                this.error('Go program execution failed:', error);
                if (this._moduleReadyReject) {
                    this._moduleReadyReject(error);
                }
            });
            
            this.log('Go WASM module initialization started, waiting for readiness...');
            
            // 等待轮询或 Go 的 ready 通知确认函数已经可调用。
            await readyPromise;
            
            this.log('Go WASM module loaded and initialized successfully');
            
        } catch (error) {
            throw new Error(`Failed to load Go WASM module: ${error.message}`);
        }
    }
    
    /**
     * 下载 WASM 文件。
     *
     * 调用来源：loadGoModule()。
     * 返回值是 WebAssembly.instantiate() 所需的二进制字节。
     */
    async fetchWasm(wasmPath) {
        try {
            const response = await fetch(wasmPath);
            if (!response.ok) {
                throw new Error(`HTTP ${response.status}: ${response.statusText}`);
            }
            return await response.arrayBuffer();
        } catch (error) {
            throw new Error(`Failed to fetch WASM file: ${error.message}`);
        }
    }
    
    /**
     * 安装 Go 消息处理包装。
     *
     * 调用来源：loadGoModule()。
     * 它不负责主线程与 Worker 的游戏消息分发；后者由 go.wasm.loader.js 和
     * WorkerMessageManager 处理。这里仅识别 Go 运行时发出的内部消息。
     */
    setupMessageHandling() {
        // 保留原始 postMessage，非 Go 消息仍然正常发给主线程。
        const originalPostMessage = self.postMessage;
        self.postMessage = (data) => {
            if (this.isGoMessage(data)) {
                this.handleGoMessage(data);
            } else {
                originalPostMessage.call(self, data);
            }
        };
    }
    
    /**
     * 判断消息是否由 Go 运行时发出。
     * 调用来源：setupMessageHandling() 的 postMessage 包装器。
     */
    isGoMessage(data) {
        return data && typeof data === 'object' && 
               (data.cmd === 'goReady' || data.source === 'go-wasm');
    }
    
    /**
     * 处理 Go 运行时发出的内部消息。
     * 调用来源：setupMessageHandling()，当前实现主要处理 ready 通知。
     */
    handleGoMessage(data) {
        switch (data.cmd) {
            case 'goReady':
                this.handleGoReady(data);
                break;
            case 'goFunction':
                this.handleGoFunctionCall(data);
                break;
            default:
                this.log('收到未知的 Go 消息:', data);
        }
    }
    
    /**
     * 处理 Go WASM 已经就绪的通知。
     * 调用来源：handleGoMessage() 的 goReady 分支。
     */
    handleGoReady(data) {
        this.isReady = true;
        this.log('Go module is ready, available functions:', data.functions);
        
        // Go 已经可调用，执行初始化期间暂存的调用。
        this.processPendingCalls();
        
        // 唤醒 initialize() 中等待 Go 就绪的 Promise。
        if (this._moduleReadyResolve) {
            this._moduleReadyResolve();
            this._moduleReadyResolve = null;
            this._moduleReadyReject = null;
        }
        
        // 通知主线程（如果当前 Worker 的宿主需要这个状态）。
        self.postMessage({
            cmd: 'goModuleReady',
            availableFunctions: data.functions || [],
            source: 'go-wasm-bridge'
        });
    }
    
    /**
     * 执行 Go 尚未就绪时排队的调用。
     * 调用来源：handleGoReady()。
     */
    processPendingCalls() {
        while (this.pendingCalls.length > 0) {
            const call = this.pendingCalls.shift();
            this.executeGoFunction(call.funcName, call.args, call.resolve, call.reject);
        }
    }
    
    /**
     * 调用一个 Go 导出函数。
     *
     * 调用来源：go.wasm.loader.js 的 handleCustomCall()/tryRunGoWasm()。
     * Go 尚未就绪时先排队，ready 后由 executeGoFunction() 执行。
     */
    callGoFunction(funcName, ...args) {
        return new Promise((resolve, reject) => {
            if (!this.isReady) {
                // Go 尚未就绪，先保存调用和 Promise 的 resolve/reject。
                this.pendingCalls.push({ funcName, args, resolve, reject });
                return;
            }
            
            this.executeGoFunction(funcName, args, resolve, reject);
        });
    }
    
    /**
     * 取得 Go 通过 syscall/js 注册到当前 Worker self 上的函数。
     *
     * 调用来源：worker.wrap.gen.js 的 BindFFI()。
     * 典型函数是 gdspx_dispatch；它不是 C++ 函数，而是 Go 暴露的 JS 函数包装器。
     */
    getGoFunction(funcName){
        const goFunc = self[funcName];
        if (typeof goFunc !== 'function') {
            console.error(`Go function ${funcName} does not exist`);
            return null
        }
        return goFunc
    }
    /**
     * 真正执行 Go 函数，并统一处理同步返回值、Promise 和超时。
     *
     * 调用来源：callGoFunction()。
     */
    executeGoFunction(funcName, args, resolve, reject) {
        try {
            // Go 的 syscall/js 会把导出函数挂到当前 Worker 的 self 上。
            const goFunc = self[funcName];
            if (typeof goFunc !== 'function') {
                reject(new Error(`Go function ${funcName} does not exist`));
                return;
            }
            
            // 防止 Go 函数长期不返回导致调用方一直等待。
            const timeoutId = setTimeout(() => {
                reject(new Error(`Go function ${funcName} call timed out`));
            }, this.config.timeout);
            
            // 直接调用当前 Worker 中的 Go JavaScript 包装函数。
            const result = goFunc(...args);
            
            // Go 函数可能同步返回，也可能返回 Promise。
            if (result && typeof result.then === 'function') {
                // Promise return value
                result
                    .then(value => {
                        clearTimeout(timeoutId);
                        resolve(value);
                    })
                    .catch(error => {
                        clearTimeout(timeoutId);
                        reject(error);
                    });
            } else {
                // Synchronous return value
                clearTimeout(timeoutId);
                resolve(result);
            }
            
        } catch (error) {
            reject(new Error(`Failed to execute Go function ${funcName}: ${error.message}`));
        }
    }
    
    /**
     * 按并发方式调用多个 Go 函数并等待全部结果。
     * 调用来源：当前 Worker 中需要批量调用 Go 的上层代码。
     */
    async callGoFunctions(calls) {
        const promises = calls.map(call => 
            this.callGoFunction(call.funcName, ...(call.args || []))
        );
        return await Promise.all(promises);
    }
    
    /**
     * 枚举当前 Worker 上已经注册的 Go 函数。
     * 调用来源：loadGoModule() 的就绪轮询，以及调试信息输出。
     */
    getAvailableGoFunctions() {
        const functions = [];
        for (const key in self) {
            if (typeof self[key] === 'function' && key.startsWith('go')) {
                functions.push(key);
            }
        }
        return functions;
    }
    
    /**
     * 带参数校验和错误处理的 Go 函数调用入口。
     * 调用来源：go.wasm.loader.js 的 go_wasm_init、ispx_build、ispx_start，
     * 以及主线程 customCall 转发。
     */
    async callGoFunctionSafe(funcName, ...args) {
        try {
            // 函数名必须是有效的字符串。
            if (!funcName || typeof funcName !== 'string') {
                throw new Error('Function name must be a valid string');
            }
            
            if (!this.isReady) {
                throw new Error('Go module is not ready');
            }
            
            // 委托给 callGoFunction() 执行，并等待返回结果。
            const result = await this.callGoFunction(funcName, ...args);
            
            // Validate result
            if (result && typeof result === 'object' && result.error) {
                throw new Error(`Go function execution error: ${result.error}`);
            }
            
            return result;
            
        } catch (error) {
            this.error(`Failed to safely call Go function ${funcName}:`, error);
            
            // Record debug information
            if (this.config.enableDebug) {
                this.log('Debug information:', {
                    funcName,
                    args,
                    isReady: this.isReady,
                    availableFunctions: this.getAvailableGoFunctions()
                });
            }
            
            throw error;
        }
    }
    
    /**
     * 使用可转移数据调用 Go 函数。
     * 调用来源：需要传输 ArrayBuffer 的 Worker 业务代码；当前实现仍复用普通调用。
     */
    async callGoFunctionWithTransfer(funcName, transferableData, ...args) {
        // 当前没有单独优化 transferable 对象，这个接口为后续优化保留。
        return this.callGoFunction(funcName, transferableData, ...args);
    }
    
    /**
     * 清理 Go WASM 桥接器状态。
     * 调用来源：需要重启或销毁 Worker 内 Go 模块的上层生命周期代码。
     */
    destroy() {
        this.log('Destroying Go WASM module instance');
        
        // 拒绝尚未执行的调用，避免调用方永久等待。
        this.pendingCalls.forEach(call => {
            call.reject(new Error('Go module has been destroyed'));
        });
        this.pendingCalls = [];
        
        // 清理已经开始但仍处于等待状态的调用。
        this.activeCalls.forEach(call => {
            call.reject(new Error('Go module has been destroyed'));
        });
        this.activeCalls.clear();
        
        // 清空实例状态；这不会自动重新创建 Go WASM。
        this.isReady = false;
        this.goInstance = null;
        this.goRuntime = null;
    }
    
    /** 输出调试日志。 */
    log(...args) {
        if (this.config.enableDebug) {
            console.log('[GoWasmBridge]', ...args);
        }
    }
    
    /** 输出错误日志。 */
    error(...args) {
        console.error('[GoWasmBridge]', ...args);
    }
}

// Export for Worker usage
if (typeof self !== 'undefined' && typeof module === 'undefined') {
    // Worker 环境使用 self 暴露构造器，go.wasm.loader.js 才能 new GoWasmBridge()。
    self.GoWasmBridge = GoWasmBridge;
} else if (typeof module !== 'undefined' && module.exports) {
    // Node.js 测试环境使用 CommonJS 导出。
    module.exports = GoWasmBridge;
} else if (typeof window !== 'undefined') {
    // 普通浏览器环境挂到 window；Worker 模式不会走这里。
    window.GoWasmBridge = GoWasmBridge;
}

/**
 * Usage example:
 * 
 * // Usage in Worker
 * const bridge = new GoWasmBridge();
 * 
 * // Initialize
 * await bridge.initialize({
 *     wasmPath: './main.wasm',
 *     runtimePath: './go.wasm.exec.js',
 *     timeout: 5000,
 *     enableDebug: true
 * });
 * 
 * // Call Go function
 * const result = await bridge.callGoFunction('goCalculateSum', 10, 20);
 * console.log('Calculation result:', result);
 * 
 * // Safe call
 * try {
 *     const safeResult = await bridge.callGoFunctionSafe('goProcessData', data);
 *     console.log('Processing result:', safeResult);
 * } catch (error) {
 *     console.error('Call failed:', error);
 * }
 * 
 * // Batch call
 * const batchResults = await bridge.callGoFunctions([
 *     { funcName: 'goFunc1', args: [1, 2] },
 *     { funcName: 'goFunc2', args: ['hello'] }
 * ]);
 */
