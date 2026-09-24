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

package gdext

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/goplus/spx/v3/internal/cmd/codegen/gdextensionparser/clang"
	"github.com/goplus/spx/v3/internal/cmd/codegen/generate/common"
)

// 默认回调直接从 ABI 的 SpxCallbackInfo 字段生成，确保新增回调后引擎的兜底表
// 不会遗留未初始化的函数指针。
func (g *Generator) writeCallbackDefaults(spxModulePath string) error {
	functions := make(map[string]clang.TypedefFunction)
	for _, function := range g.AST().CollectGDExtensionICallbackFunctions() {
		functions[function.Name] = function
	}
	var callbackStruct *clang.TypedefStruct
	for _, expr := range g.AST().Expr {
		if expr.Struct != nil && expr.Struct.Name == "SpxCallbackInfo" {
			callbackStruct = expr.Struct
			break
		}
	}
	if callbackStruct == nil {
		return fmt.Errorf("generate callback defaults: missing SpxCallbackInfo")
	}
	var out strings.Builder
	out.WriteString(`// 由 SPX 代码生成器自动生成，请勿直接编辑。
// 本文件根据 gdextension_spx_ext.h 中的回调 typedef 和 SpxCallbackInfo 自动生成。
// 每个字段都会获得一个参数签名完全一致的空 lambda，供 Go 尚未注册真实回调时使用；
// 需要新增或修改回调时应修改 gdextension_spx_ext.h.tmpl，而不是编辑本文件。
#ifndef SPX_CALLBACK_DEFAULTS_GEN_H
#define SPX_CALLBACK_DEFAULTS_GEN_H

#include "gdextension_spx_ext.h"

// 构造一张可安全调用的默认回调表，避免生命周期早期、关闭期间或未连接 Go 时
// 因空函数指针而崩溃。注册真实回调后，SpxEngine 会用调用方提供的表替换它。
inline SpxCallbackInfo get_default_spx_callbacks() {
	// 先将整个结构清零，随后生成器为每一个已声明字段安装同签名空函数。
	SpxCallbackInfo callbacks = {};
`)
	for _, field := range callbackStruct.Fields {
		if field.Variable == nil {
			return fmt.Errorf("generate callback defaults: unsupported inline callback field")
		}
		variable := field.Variable
		function, ok := functions[variable.Type.Name]
		if !ok || variable.Type.IsPointer || function.ReturnType.CStyleString() != "void" {
			return fmt.Errorf("generate callback defaults: unsupported callback %s", variable.Name)
		}
		args := make([]string, len(function.Arguments))
		for i, arg := range function.Arguments {
			args[i] = arg.Type.CStyleString()
		}
		fmt.Fprintf(&out, "\tcallbacks.%s = [](%s) {};\n", variable.Name, strings.Join(args, ", "))
	}
	out.WriteString("\treturn callbacks;\n}\n\n#endif // SPX_CALLBACK_DEFAULTS_GEN_H\n")
	return common.WriteGeneratedFile(filepath.Join(spxModulePath, "spx_callback_defaults.gen.h"), []byte(out.String()), 0o644)
}
