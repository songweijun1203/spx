/*
 * 输入 Action 名称桥接。
 *
 * Godot 脚本通常用字符串查询输入，例如 "jump"、"ui_accept"。这里把名称交给
 * Godot 注册为整数 ID；Go 侧按 epoch 缓存名称到 ID 的映射，后续状态查询只传 ID。
 *
 * globalThis 是当前 JavaScript 全局对象：网页一般对应 window，Worker 对应 self。
 */

// Action ID 只对注册它的 Godot Module 实例有效。Module 重启或输入注册表重置后，
// epoch 递增，Go 侧据此丢弃自己的旧 ID 缓存。
const inputActionRegistry = {
    module: null, // 当前注册表对应的 Godot WASM Module。
    epoch: 0,     // 注册表每次失效时递增。
    bridge: null, // 必要时创建并复用的 GdspxFuncs 实例。
};

// 显式使已分配的 Action ID 失效。Godot reset 会通过 JS Library 调用这里。
function GdspxResetInputActions() {
    inputActionRegistry.epoch += 1;
    inputActionRegistry.bridge = null;
}

// 取得能执行 gdspx_input_register_action 的对象。
function GetInputBridge() {
    // 普通模式下，game.js 已把 GdspxFuncs 的方法绑定到全局对象，优先直接使用。
    if (typeof globalThis['gdspx_input_register_action'] === 'function') {
        return globalThis;
    }
    // 某些加载路径没有逐个挂载桥方法，此时自行创建并复用桥接实例。
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

// 确认注册表属于当前 Module；如果 Godot 已重启，则发布新的 epoch。
function EnsureInputActionRegistry() {
    if (!HasActiveModule()) {
        return false;
    }
    if (inputActionRegistry.module !== Module) {
        inputActionRegistry.module = Module;
        GdspxResetInputActions();
    }
    return true;
}

function GdspxGetInputActionEpoch() {
    EnsureInputActionRegistry();
    return inputActionRegistry.epoch;
}

// 根据 Action 名称向 Godot 查询稳定整数 ID；跨语言缓存由 Go 侧按 epoch 管理。
function GdspxGetInputActionID(action) {
    if (!EnsureInputActionRegistry()) {
        return -1;
    }
    const bridge = GetInputBridge();
    const call = bridge && bridge['gdspx_input_register_action'];
    if (typeof call !== 'function') {
        return -1;
    }

    // call.call 显式把函数内部的 this 设为 bridge。
    return ToStableActionID(call.call(bridge, action));
}

// 使用字符串键导出，确保压缩后 Go 的 syscall/js 仍能按固定名称找到这些函数。
globalThis['GdspxResetInputActions'] = GdspxResetInputActions;
globalThis['GdspxGetInputActionEpoch'] = GdspxGetInputActionEpoch;
globalThis['GdspxGetInputActionID'] = GdspxGetInputActionID;
