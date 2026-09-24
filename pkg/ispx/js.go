//go:build js && wasm

/*
 * Copyright (c) 2021 The XGo Authors (xgo.dev). All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package ispx

import (
	"errors"
	"fmt"
	"syscall/js"
	_ "unsafe"

	spx "github.com/goplus/spx/v3"
	spxlog "github.com/goplus/spx/v3/internal/log"
)

// JavaScript built-in types.
var (
	jsTypeObject      = js.Global().Get("Object")
	jsTypeError       = js.Global().Get("Error")
	jsTypeArrayBuffer = js.Global().Get("ArrayBuffer")
	jsTypeUint8Array  = js.Global().Get("Uint8Array")
)

// 在 ispx.wasm 的 Go 包初始化阶段，把项目编译、启动、停止等宿主 API 注册到当前 globalThis。
// 最近调用方：Go runtime 在执行 cmd/ispx.main() 前自动运行包 init，无普通 Go 调用者。
// 最顶层入口：普通模式的 GameApp.runLogicWasm()，或 Worker 的 GoWasmBridge.initialize()。
func init() {
	js.Global().Set("ispx_build", jsFuncOfWithError(ispxBuild))
	js.Global().Set("ispx_start", jsFuncOfWithError(ispxStart))
	js.Global().Set("ispx_stop", jsFuncOfWithError(ispxStop))
	js.Global().Set("ispx_input_recording_finish", jsFuncOfWithError(ispxInputRecordingFinish))
	js.Global().Set("ispx_input_session_status", jsFuncOfWithError(ispxInputSessionStatus))
}

// defaultIXGoContextLookup is the default [ixgo.Context.Lookup] when none is
// provided. It reports a package import error and requests a runtime reset.
func defaultIXGoContextLookup(root, path string) (dir string, found bool) {
	reportRuntimeError(fmt.Errorf("failed to resolve package import %q", path))
	return
}

// ispxBuild 是 [Build] 的 JavaScript 入口，负责把 JS 项目文件对象转换成 Go map 后编译。
// 最近调用方：普通模式 GameApp.buildGame()，或 Worker tryRunGoWasm()。
// 最顶层入口：外层页面下载 game.zip 后调用 GameApp.InitGame()/StartGame()。
func ispxBuild(this js.Value, args []js.Value) any {
	if len(args) == 0 {
		return errors.New("missing files argument")
	}

	filesArg := args[0]
	if !isPlainJSObject(filesArg) {
		return errors.New("invalid files argument type")
	}

	files, err := convertJSFilesToMap(filesArg)
	if err != nil {
		return fmt.Errorf("failed to convert files: %w", err)
	}

	if err := Build(files); err != nil {
		return fmt.Errorf("failed to build: %w", err)
	}
	return nil
}

// ispxStart 从 JavaScript 启动 XGo 解释器游戏。
// 最近调用方：Worker 的 tryRunGoWasm() 或普通模式 GameApp.onRunAfterStart()。
// 最顶层入口：用户点击 Start，或宿主调用 runner.html 的 window.startGame()。
// 它在 goroutine 中调用 Run()；Run() 执行游戏 main.go，随后进入
// internal/engine.Main()，由 gdengine.PrepareLink() 建立 Web FFI、Manager 和回调。
func ispxStart(this js.Value, args []js.Value) any {
	preparation, err := prepareHostInputSession(args)
	if err != nil {
		return err
	}
	go func() {
		defer cancelPreparedHostInputSession(preparation)
		defer func() {
			if r := recover(); r != nil {
				reportRuntimeError(fmt.Errorf("interpreter exited with panic: %v", r))
			}
		}()

		exitCode, err := Run()
		if err != nil {
			reportRuntimeError(fmt.Errorf("interpreter exited with code %d: %w", exitCode, err))
			return
		}
	}()
	return nil
}

func prepareHostInputSession(args []js.Value) (spx.InputSessionPreparation, error) {
	if len(args) == 0 || args[0].Type() == js.TypeUndefined || args[0].Type() == js.TypeNull {
		return spx.InputSessionPreparation{}, nil
	}
	input := args[0]
	if !isPlainJSObject(input) {
		return spx.InputSessionPreparation{}, errors.New("input session must be an object")
	}
	mode := input.Get("mode")
	if mode.Type() != js.TypeString {
		return spx.InputSessionPreparation{}, errors.New("input session mode must be a string")
	}
	options, err := hostInputSessionOptions(input)
	if err != nil {
		return spx.InputSessionPreparation{}, err
	}
	switch mode.String() {
	case "record":
		fps := float64(defaultHostInputRecordingFPS)
		value := input.Get("fps")
		if value.Type() != js.TypeUndefined && value.Type() != js.TypeNull {
			if value.Type() != js.TypeNumber {
				return spx.InputSessionPreparation{}, errors.New("input recording FPS must be a number")
			}
			fps = value.Float()
		}
		return prepareHostInputRecording(fps, options)
	case "replay":
		value := input.Get("data")
		if value.Type() == js.TypeUndefined || value.Type() == js.TypeNull {
			return spx.InputSessionPreparation{}, errors.New("missing input replay data")
		}
		data, err := copyJSReplayData(value)
		if err != nil {
			return spx.InputSessionPreparation{}, err
		}
		return prepareHostInputReplay(data, options)
	default:
		return spx.InputSessionPreparation{}, fmt.Errorf("unsupported input session mode %q", mode.String())
	}
}

func hostInputSessionOptions(input js.Value) (spx.InputSessionOptions, error) {
	value := input.Get("captureKey")
	if value.Type() == js.TypeUndefined || value.Type() == js.TypeNull {
		return spx.InputSessionOptions{}, nil
	}
	if value.Type() != js.TypeString {
		return spx.InputSessionOptions{}, errors.New("input session captureKey must be a key name string")
	}
	key := spx.KeyFromString(value.String())
	if key == spx.KeyMax || key == spx.KeyAny {
		return spx.InputSessionOptions{}, fmt.Errorf("unsupported input session captureKey %q", value.String())
	}
	return spx.InputSessionOptions{CaptureKey: key}, nil
}

func ispxInputRecordingFinish(this js.Value, args []js.Value) any {
	data, err := finishHostInputRecording()
	if err != nil {
		return err
	}
	return copyBytesToJS(data)
}

func ispxInputSessionStatus(this js.Value, args []js.Value) any {
	status := getHostInputSessionStatus()
	result := jsTypeObject.New()
	result.Set("mode", status.Mode)
	result.Set("phase", status.Phase)
	result.Set("completed", status.Completed)
	result.Set("exhausted", status.Exhausted)
	result.Set("currentTick", nil)
	if status.HasCurrentTick {
		result.Set("currentTick", float64(status.CurrentTick))
	}
	result.Set("nextFrame", float64(status.NextFrame))
	result.Set("frameCount", status.FrameCount)
	if status.Error != "" {
		result.Set("error", status.Error)
	}
	return result
}

func copyJSReplayData(value js.Value) ([]byte, error) {
	switch {
	case value.Type() == js.TypeString:
		return []byte(value.String()), nil
	case value.InstanceOf(jsTypeUint8Array):
		data := make([]byte, value.Get("byteLength").Int())
		js.CopyBytesToGo(data, value)
		return data, nil
	case value.InstanceOf(jsTypeArrayBuffer):
		bytes := jsTypeUint8Array.New(value)
		data := make([]byte, bytes.Get("byteLength").Int())
		js.CopyBytesToGo(data, bytes)
		return data, nil
	default:
		return nil, errors.New("expected string, Uint8Array, or ArrayBuffer")
	}
}

func copyBytesToJS(data []byte) js.Value {
	result := jsTypeUint8Array.New(len(data))
	js.CopyBytesToJS(result, data)
	return result
}

// ispxStop 异步停止解释器；使用 goroutine 是为了不阻塞 JavaScript 主线程。
// 最近调用方：GameApp.stopGame()；最顶层入口：用户点击 Stop 或 runtime 恢复流程。
// Shutdown() 最终请求 C++ SpxEngine reset，并等待解释器游戏退出。
func ispxStop(this js.Value, args []js.Value) any {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportRuntimeError(fmt.Errorf("shutdown exited with panic: %v", r))
			}
		}()

		if err := Shutdown(); err != nil {
			reportRuntimeError(fmt.Errorf("failed to shutdown: %w", err))
			return
		}
	}()
	return nil
}

// reportRuntimeError reports a runtime error to JavaScript and requests a reset.
func reportRuntimeError(err error) {
	spxlog.Error("%v", err)
	js.Global().Call("gdspx_ext_on_runtime_panic", err.Error())
	js.Global().Call("gdspx_ext_request_reset", 1)
}

// jsFuncOfWithError is like [js.FuncOf] but wraps Go errors as JavaScript Error objects.
func jsFuncOfWithError(fn func(this js.Value, args []js.Value) any) js.Func {
	return js.FuncOf(func(this js.Value, args []js.Value) any {
		result := fn(this, args)
		if err, ok := result.(error); ok {
			return jsTypeError.New(err.Error())
		}
		return result
	})
}

// isPlainJSObject reports whether v is a plain JavaScript object, not an array,
// typed array, or other built-in object type.
func isPlainJSObject(v js.Value) bool {
	return jsTypeObject.Get("prototype").Get("toString").Call("call", v).String() == "[object Object]"
}

// convertJSFilesToMap converts a JavaScript object mapping file paths to
// Uint8Array or ArrayBuffer into a Go map.
func convertJSFilesToMap(input js.Value) (map[string][]byte, error) {
	keys := jsTypeObject.Call("keys", input)
	n := keys.Length()
	files := make(map[string][]byte, n)
	for i := range n {
		name := keys.Index(i).String()

		var jsData js.Value
		switch v := input.Get(name); {
		case v.InstanceOf(jsTypeUint8Array):
			jsData = v
		case v.InstanceOf(jsTypeArrayBuffer):
			jsData = jsTypeUint8Array.New(v)
		default:
			return nil, fmt.Errorf("unsupported file value type for %q", name)
		}
		length := jsData.Get("length").Int()
		data := make([]byte, length)
		js.CopyBytesToGo(data, jsData)

		files[name] = data
	}
	return files, nil
}
