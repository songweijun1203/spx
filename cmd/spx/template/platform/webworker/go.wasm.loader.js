
// 处理主线程通过 WorkerMessageManager 发来的游戏控制消息。
// 调用来源：导出阶段注入到 engine.js 的 Worker message handler。
function handleGameAppMessage(data) {
  const workerId = (typeof Module !== 'undefined' && Module['workerID']) || 'unknown';
  const threadInfo = typeof importScripts !== 'undefined' ? 'Worker' : 'MainThread';
  try {
    switch (data.cmd) {
      case 'projectDataUpdate':
        handleProjectDataUpdate(data);
        break;
      case 'customCall':
        handleCustomCall(data);
        break;
      case 'callResponse':
        handleCallResponse(data);
        break;
      default:
        console.warn(`[Thread ${threadInfo}-${workerId}] Unknown GameApp command:`, data.cmd || data.type);
        break;
    }
  } catch (error) {
    console.error(`[Thread ${threadInfo}-${workerId}] Error handling GameApp message:`, error);
  }
}

// 保存项目数据；收到这条消息后才开始加载 Go WASM。
// 调用来源：handleGameAppMessage() 的 projectDataUpdate 分支。
async function handleProjectDataUpdate(data) {
  // godot_js_spx_on_engine_start 可能在这条消息之前执行；如果当时没有
  // gameAssetURLs，initExtensionWasm() 会提前返回。这里补齐资源地址后再次调用，
  // 才会真正进入 loadGoWasmModule()。
  Module["gameProjectData"] = data.data;
  Module["gameAssetURLs"] = data.gameAssetURLs;
  initExtensionWasm()
}

// 在 Worker 内调用 Go 导出的函数，例如 ispx_build 或 ispx_start。
// 调用来源：handleGameAppMessage() 的 customCall 分支。
async function handleCustomCall(data) {
  var infos = data.data
  var funcName = infos.funcName
  try {
    // 检查参数中是否包含需要代理到主线程的回调函数。
    var processedArgs = processMainThreadCallbacks(infos.args);

    var result = await self.goBridge.callGoFunctionSafe(funcName, ...processedArgs);
    var param = result == null ? "" : result
    // TODO implement return result
    //postMessage({
    //  cmd: 'callHandler',
    //  handler: '_onWorkerCb_' + funcName,
    //  args: [param]
    //});
  } catch (error) {
    console.error("Error in " + funcName + ":", error);
  }
}

// 把主线程回调标记转换为 Worker 内可调用的 Promise 代理函数。
// 调用来源：handleCustomCall()。
function processMainThreadCallbacks(args) {
  var processedArgs = [];
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "_SPX_CALLBACK_FUNC_" && i + 1 < args.length) {
      // 标记后的下一个参数是主线程回调名称。
      var callbackName = args[i + 1];
      // 创建一个向主线程发消息的代理函数。
      var proxyFunction = createMainThreadCallbackProxy(callbackName);
      processedArgs.push(proxyFunction);
      i++; // 跳过已经消费的回调名称。
    } else {
      processedArgs.push(args[i]);
    }
  }
  return processedArgs;
}

// 请求主线程执行一个回调。
// 调用来源：tryRunGoWasm() 的 on_game_started，以及回调代理函数。
function callMainThread(callbackName, args) {
  postMessage({
    cmd: 'callHandler',
    handler: "_spxOnMainCall",
    args: args ? [callbackName, ...args] : [callbackName]
  });
}

// 创建一个异步的主线程回调代理。
// 调用来源：processMainThreadCallbacks()。
function createMainThreadCallbackProxy(callbackName) {
  return function (...args) {
    return new Promise((resolve, reject) => {
      const requestId = ++tokenRequestId;

      // 保存 Promise 的完成函数，等待主线程返回结果。
      pendingTokenRequests.set(requestId, { resolve, reject });

      // 通过 postMessage 请求主线程执行回调。
      callMainThread(callbackName, [requestId, ...args]);

      // 主线程超时后拒绝 Promise，避免 Go 一直阻塞等待。
      setTimeout(() => {
        if (pendingTokenRequests.has(requestId)) {
          pendingTokenRequests.delete(requestId);
          reject(new Error(`Callback ${callbackName} timeout`));
        }
      }, 10000);
    });
  };
}

// Go WASM 初始化完成后，绑定 Go → Godot 的 gdspx_* 方法，并启动游戏逻辑。
// 调用来源：initExtensionWasm() 在 loadGoWasmModule() 成功后调用。
function tryRunGoWasm() {
  const workerId = (typeof Module !== 'undefined' && Module['workerID']) || 'unknown';
  if (!Module["FFI"]) {
    return;
  }
  if (!Module["gameProjectData"]) {
    return;
  }
  
  const spxfuncs = new GdspxFuncs();
  const methodNames = Object.getOwnPropertyNames(Object.getPrototypeOf(spxfuncs));
  methodNames.forEach(key => {
      if (key.startsWith('gdspx_') && typeof spxfuncs[key] === 'function') {
          self[key] = spxfuncs[key].bind(spxfuncs);
      }
  });
  self['Module'] = Module;

  if (self.goBridge && self.goBridge.isReady) {
    try {
      // Go 已就绪后，传入项目数据并启动游戏逻辑。
      self.goBridge.callGoFunctionSafe('ispx_build', Module["gameProjectData"]);
      self.goBridge.callGoFunctionSafe('ispx_start');
      callMainThread('on_game_started');
    } catch (error) {
      console.error(`[Worker ${workerId}] Error calling Go function to process project data:`, error);
    }
  }
}

// 初始化 Worker 内的 Go WASM。
// 调用来源：library_godot_gdspx.js 的 godot_js_spx_on_engine_start()。
// 这是 Godot 启动后触发 Go 加载的入口，不是浏览器页面直接调用的入口。
async function initExtensionWasm() {
  // 如果项目资源地址还没有传入 Worker，
  // 当前只结束本次尝试，不加载 ispx.wasm。
  if (Module["gameAssetURLs"] == undefined) {
    return;
  }
  const workerId = Module['workerID'] || 'main';
  const threadInfo = typeof importScripts !== 'undefined' ? 'Worker' : 'MainThread';

  // 防止初始化过程中使用旧的 FFI
  globalThis['FFI'] = null

  try {
    // 第一次由 Godot 的 on_engine_start 回调进入；如果项目数据尚未到达，
    // 上面的 gameAssetURLs 检查会结束本次调用。收到 projectDataUpdate 后，
    // handleProjectDataUpdate() 会再次进入这里。
    await loadGoWasmModule();
    // loadGoWasmModule() 成功后，Module.FFI 才应包含 gdspx_dispatch。
    // library_godot_gdspx.js 的 dispatch() 会读取当前 Worker 的 globalThis.FFI。
    globalThis['FFI'] = Module["FFI"];

    // 注册 Go → Godot 的 gdspx_* 函数，
    // 并执行 ispx_build()、ispx_start()
    tryRunGoWasm()
    return true;
  } catch (error) {
    console.error(`[Thread ${threadInfo}-${workerId}] Go WASM initialization failed:`, error);
    return false;
  }
}

// AI Token Provider 相关的请求编号和等待表。
let tokenRequestId = 0;
const pendingTokenRequests = new Map();

// 现在统一使用通用回调代理，不再需要单独的 requestTokenFromMainThread。

function handleCallResponse(data) {
  if (data.responseId) {
    const requestId = parseInt(data.responseId);
    if (pendingTokenRequests.has(requestId)) {
      const { resolve, reject } = pendingTokenRequests.get(requestId);
      pendingTokenRequests.delete(requestId);

      if (data.error) {
        reject(new Error(data.error));
      } else {
        resolve(data.result || "");
      }
      return;
    }
    console.error("handleCallResponse: no pendingTokenRequests", data)
  }
  console.error("handleCallResponse: no responseId", data)
}

// 暴露给 Godot Web 运行时；library_godot_gdspx.js 会在引擎启动回调中调用它。
if (typeof self !== 'undefined') {
  self['initExtensionWasm'] = initExtensionWasm;
}


// 创建 GoWasmBridge 并加载 ispx.wasm。
// 调用来源：initExtensionWasm()。
async function loadGoWasmModule() {
  // 如果当前 Worker 已经加载过 Go，就复用已有桥接器。
  if (self.goBridge && self.goBridge.isReady) {
    console.log(`[Godot Worker ${Module['workerID']}] Go WASM is already loaded, using directly`);
    return;
  }

  try {
    // GoWasmBridge 只管理 Go WASM 的加载和函数调用，不创建 Worker。
    const goBridge = new GoWasmBridge();

    let assetURLs = Module["gameAssetURLs"];
    // 内部依次执行 new Go()、实例化 ispx.wasm 和 goRuntime.run()。
    await goBridge.initialize({
      wasmPath: assetURLs["ispx.wasm"],
      timeout: 15000,
      enableDebug: false
    });

    // Go 运行后先执行初始化握手；成功后把 Go 的 gdspx_dispatch
    // 包装成 Module.FFI，供 library_godot_gdspx.js 调用。
    try {
      const initResult = await goBridge.callGoFunctionSafe('go_wasm_init');
      Module['FFI'] = BindFFI(goBridge);
      // 通知主线程 Go WASM 已加载；这不是 Godot ↔ Go 的事件分发本身。
      callMainThread('on_wasm_loaded');
    } catch (goInitError) {
      console.warn(`[Godot Worker ${Module['workerID']}] Go initialization function call failed, but continuing execution:`, goInitError);
    }

    // 保存到当前 Worker 的 self，供 handleCustomCall()/tryRunGoWasm() 复用。
    self.goBridge = goBridge;
  } catch (error) {
    console.error(`[Godot Worker ${Module['workerID']}] Go WASM module loading failed:`, error);
    throw error;
  }
}
