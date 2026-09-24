/*
 * Closure Compiler 的“外部符号声明”文件。
 *
 * 这两行看起来像普通变量定义，但这里只用于告诉压缩器：Module 和 miniEngine
 * 会由别处提供，请不要删除、重命名或假设它们不存在。该文件本身没有业务逻辑。
 *
 * - Module：Emscripten 创建的 Godot WASM 运行时对象。
 * - miniEngine：小游戏/小程序运行环境注入的宿主对象。
 *
 * var 是 JavaScript 的变量声明关键字。这里不能改成具体值，否则会把“声明外部
 * 名称”的含义变成运行时赋值。
 */
var Module;
var miniEngine;
