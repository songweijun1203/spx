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

package project

import "slices"

// defaultDisplayFontPath 是 SPX 自带的基础字体。它与项目字体分开，使保留的
// default 字体族不依赖项目是否提供中文或其他字体。
const defaultDisplayFontPath = "res://engine/fonts/default.ttf"

type RuntimeFontFace struct {
	Path   string
	Family string
}

// RuntimeFontPlan 是资源路径已经转换、但尚未摊平成跨语言数组的完整字体应用计划。
type RuntimeFontPlan struct {
	DefaultPath string
	Faces       []RuntimeFontFace
	Preferences []string
}

func (p RuntimeFontPlan) Clone() RuntimeFontPlan {
	p.Faces = slices.Clone(p.Faces)
	p.Preferences = slices.Clone(p.Preferences)
	return p
}

// ResolveRuntimeFontPlan 把已校验的项目字体目录转换为 Godot 可加载的资源路径，
// 同时保留字体族名和回退优先级。真正修改 Godot Theme 的动作由 ResMgr 完成。
func ResolveRuntimeFontPlan(fonts ProjectFonts, resolvePath func(string) string) RuntimeFontPlan {
	faceCount := 0
	for _, family := range fonts.Families {
		faceCount += len(family.Faces)
	}

	plan := RuntimeFontPlan{
		DefaultPath: defaultDisplayFontPath,
		Faces:       make([]RuntimeFontFace, 0, faceCount),
		Preferences: slices.Clone(fonts.Preferences),
	}
	for _, family := range fonts.Families {
		for _, face := range family.Faces {
			path := face.Path
			if resolvePath != nil {
				path = resolvePath(path)
			}
			plan.Faces = append(plan.Faces, RuntimeFontFace{
				Path:   path,
				Family: family.Name,
			})
		}
	}
	return plan
}
