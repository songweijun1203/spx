/*
 * 输入 Action 名称缓存。
 *
 * Godot 脚本通常用字符串查询输入，例如 "jump"、"ui_accept"。跨 Go WASM、
 * JavaScript 和 Godot WASM 反复传字符串成本较高，所以这里第一次把名称注册为
 * 整数 ID，后续查询只传 ID。
 *
 * 初学者阅读提示：
 * - const 声明“变量绑定不再改指向”；对象内部字段仍然可以修改。
 * - new Map() 创建键值表，has/get/set/clear 分别表示查询、读取、写入、清空。
 * - function Name(...) { ... } 定义函数。
 * - typeof value === 'function' 用来判断一个值是不是函数。
 * - globalThis 是当前 JavaScript 全局对象：网页一般对应 window，Worker 对应 self。
 */

// Action ID 只对“注册它的那一个 Godot Module 实例”有效。Godot 重启后 Module
// 会变，所以缓存必须跟随 Module 一起失效。
const inputActionRegistry = {
    module: null,   // 当前缓存对应的 Godot WASM Module。
    epoch: 0,       // Module 每变化一次就递增；Go 侧据此清理自己的 ID 缓存。
    ids: new Map(), // action 字符串 -> Godot 返回的整数 ID。
    bridge: null,   // 必要时创建并复用的 GdspxFuncs 实例。
};

// 取得能执行 gdspx_input_register_action 的对象。
function GetInputBridge() {
    // 普通模式下，game.js 已把 GdspxFuncs 的方法绑定到 globalThis，优先直接使用。
    if (typeof globalThis['gdspx_input_register_action'] === 'function') {
        return globalThis;
    }
    // 某些加载路径没有把方法逐个挂到全局对象，此时自行创建桥接实例。
    // && 是“并且”；只有左侧成立时才继续计算右侧。
    if (!inputActionRegistry.bridge && typeof GdspxFuncs === 'function') {
        inputActionRegistry.bridge = new GdspxFuncs();
    }
    return inputActionRegistry.bridge;
}

function ToStableActionID(value) {
    // Go 的 syscall/js 会把 int64 表示成 { low, high }，Action ID 只取低 32 位。
    if (value && typeof value['low'] === 'number') {
        return value['low'] | 0;
    }
    // Emscripten 也可能直接返回 BigInt；asIntN(32, ...) 把它截成有符号 32 位数。
    if (typeof value === 'bigint') {
        return Number(BigInt.asIntN(32, value));
    }
    // “| 0” 是常见的 JavaScript 写法，用于把 Number 转成 32 位整数。
    return Number(value) | 0;
}

// 确认缓存属于当前 Module；如果 Godot 已重启，则清空旧缓存。
function EnsureInputActionRegistry() {
    if (!HasActiveModule()) {
        return false;
    }
    if (inputActionRegistry.module !== Module) {
        inputActionRegistry.module = Module;
        inputActionRegistry.epoch += 1;
        inputActionRegistry.ids.clear();
        inputActionRegistry.bridge = null;
    }
    return true;
}

function GdspxGetInputActionEpoch() {
    EnsureInputActionRegistry();
    return inputActionRegistry.epoch;
}

// 根据 Action 名称返回稳定的整数 ID。首次查询会跨桥调用 Godot 注册，之后命中缓存。
function GdspxGetInputActionID(action) {
    if (!EnsureInputActionRegistry()) {
        return -1;
    }
    if (inputActionRegistry.ids.has(action)) {
        return inputActionRegistry.ids.get(action);
    }

    const bridge = GetInputBridge();
    // bridge && ... 表示 bridge 存在时才读取它的方法，避免对 null 取属性。
    const call = bridge && bridge['gdspx_input_register_action'];
    if (typeof call !== 'function') {
        return -1;
    }

    // call.call(bridge, action) 调用函数，并显式把函数内部的 this 设为 bridge。
    const id = ToStableActionID(call.call(bridge, action));
    if (id >= 0) {
        inputActionRegistry.ids.set(action, id);
    }
    return id;
}

// 用字符串键导出，确保压缩后 Go 的 syscall/js 仍能按固定名称找到这两个函数。
globalThis['GdspxGetInputActionEpoch'] = GdspxGetInputActionEpoch;
globalThis['GdspxGetInputActionID'] = GdspxGetInputActionID;
