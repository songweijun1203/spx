//go:build pure_engine

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

package facade

import (
	"github.com/goplus/spx/v3/pkg/spx/pkg/engine"
)

// LinkFFI 建立不连接 Godot 的 pure_engine 会话。
// 平台：仅启用 pure_engine 构建标签时编译；直接上级为 gdengine.PrepareLink()。
// 返回的会话会立即就绪，第二个返回值固定为 true，以沿用无 Godot 回调的解释器流程。
func LinkFFI() (*LinkSession, bool) {
	return newLinkSession(func(ready func()) {
		ready()
	}, nil), true
}

// RegisterCallbacks 在 pure_engine 模式下无需注册平台回调。
// 直接上级：internal/gdengine.PrepareLink()。
func RegisterCallbacks(_ engine.CallbackInfo) {

}
