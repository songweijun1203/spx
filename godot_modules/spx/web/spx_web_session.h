#ifndef SPX_WEB_SESSION_H
#define SPX_WEB_SESSION_H

// 通知 JS/Go 桥开始接收新的碰撞会话事件，并清空上一局的延迟批次。
// 直接调用方：SpxEngine::restart；顶层调用方：浏览器 GameApp.startGame 重跑游戏。
// 实现位于 library_godot_gdspx.js 的 Emscripten JS Library 导入；extern "C" 固定
// 符号名，__sig:'v' 固定 WASM 签名。函数不传指针，因此没有线性内存所有权转移。
extern "C" void godot_js_spx_contact_session_start();

#endif // SPX_WEB_SESSION_H
