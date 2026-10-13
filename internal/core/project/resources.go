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

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"syscall"

	spxfs "github.com/goplus/spx/v3/fs"
	spxlog "github.com/goplus/spx/v3/internal/log"
)

type OpenedBuilderResources struct {
	// AssetDir 是传入资源的逻辑根（启动时通常为 "assets"），用于建立引擎资源路径。
	AssetDir string
	// FS 是统一后的项目配置视图；可能是普通目录，也可能是 packedConfigDir 包装器。
	// 所有权交给调用方，Game 会在运行期继续用它惰性加载声音等配置。
	FS spxfs.Dir
	// LoadedBuilderProject 同时包含运行参数、项目配置和已经校验的字体目录。
	LoadedBuilderProject
}

type LoadedSpriteConfig struct {
	BaseDir string
	Config  SpriteConfig
}

type LoadedSoundConfig struct {
	BaseDir string
	Config  SoundConfig
}

func AssetDirFromResource(resource any) (string, bool) {
	switch v := resource.(type) {
	case string:
		return v, true
	case spxfs.GdDir:
		return strings.TrimSuffix(v.GetPath(), "/"), true
	default:
		return "", false
	}
}

// ResourceDir 把外部资源参数统一为 spxfs.Dir。已有 Dir 原样使用；字符串会根据
// schema 交给 fs.Open，例如 asset/zip 等实现由对应包的 init 注册。
func ResourceDir(resource any) (spxfs.Dir, error) {
	if fs, ok := resource.(spxfs.Dir); ok {
		return fs, nil
	}
	path, ok := resource.(string)
	if !ok {
		return nil, fmt.Errorf("unsupported resource type %T", resource)
	}
	return spxfs.Open(path)
}

// OpenBuilderResources 是运行时项目资源的总入口。
//
// 顺序为：打开目录 -> 可选包装 index_pack.json -> 解析项目 index.json ->
// 归一化资源路径 -> 扫描并校验项目字体。任何步骤失败都会关闭刚打开的 FS，
// 成功时 FS 保持打开并由上层 Game 持有。
func OpenBuilderResources(resource any, gameConf *Config) (OpenedBuilderResources, error) {
	var opened OpenedBuilderResources
	opened.AssetDir, _ = AssetDirFromResource(resource)

	fs, err := ResourceDir(resource)
	if err != nil {
		return OpenedBuilderResources{}, err
	}
	fs = adaptConfigDir(fs)
	index, packed, err := loadPackedConfigIndex(fs)
	if err != nil {
		fs.Close()
		return OpenedBuilderResources{}, err
	}
	if packed {
		fs = &packedConfigDir{Dir: fs, index: index}
	}
	opened.FS = fs

	loaded, err := LoadBuilderProject(fs, gameConf)
	if err != nil {
		fs.Close()
		return OpenedBuilderResources{}, err
	}
	opened.LoadedBuilderProject = loaded
	return opened, nil
}

// LoadJSON 从统一配置视图读取一个且仅一个 JSON 值。视图可能组合源码 FS、
// Godot 资源回退和 index_pack.json，因此调用方无需区分部署格式。
func LoadJSON(ret any, fs spxfs.Dir, file string) error {
	fs = adaptConfigDir(fs)
	f, err := fs.Open(file)
	if err != nil {
		spxlog.Error("Failed to open file %s: %v", file, err)
		return err
	}
	defer f.Close()
	return decodeJSON(f, ret)
}

func LoadConfig(ret any, fs spxfs.Dir, index any) error {
	switch v := index.(type) {
	case io.Reader:
		return decodeJSON(v, ret)
	case string:
		return LoadJSON(ret, fs, v)
	case nil:
		return LoadJSON(ret, fs, "index.json")
	default:
		return syscall.EINVAL
	}
}

// LoadSpriteConfig 读取 sprites/<name>/index.json，并把服装的相对路径转换为相对
// 项目 assets 根的规范路径。它只解析元数据，不在此处加载图片像素或创建 Godot Resource。
func LoadSpriteConfig(fs spxfs.Dir, name string) (LoadedSpriteConfig, error) {
	baseDir := path.Join("sprites", name) + "/"
	var conf SpriteConfig
	if err := LoadJSON(&conf, fs, baseDir+"index.json"); err != nil {
		return LoadedSpriteConfig{}, err
	}
	normalizeSpriteConfigPaths(&conf, strings.TrimSuffix(baseDir, "/"))
	return LoadedSpriteConfig{
		BaseDir: baseDir,
		Config:  conf,
	}, nil
}

// LoadSoundConfig 按需读取 sounds/<name>/index.json 并归一化媒体路径。
// 音频文件本身直到 AudioMgr.PlayWithAttenuation 时才由 Godot 加载/播放。
func LoadSoundConfig(fs spxfs.Dir, name string) (LoadedSoundConfig, error) {
	baseDir := path.Join("sounds", name)
	var conf SoundConfig
	if err := LoadJSON(&conf, fs, path.Join(baseDir, "index.json")); err != nil {
		return LoadedSoundConfig{}, err
	}
	conf.Path = NormalizeConfigPath(baseDir, conf.Path)
	return LoadedSoundConfig{
		BaseDir: baseDir,
		Config:  conf,
	}, nil
}

func decodeJSON(r io.Reader, ret any) error {
	dec := json.NewDecoder(r)
	if err := dec.Decode(ret); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected content after JSON value")
		}
		return err
	}
	return nil
}

func gdAssetDir(fs spxfs.Dir) (string, bool) {
	gdDir, ok := fs.(spxfs.GdDir)
	if !ok {
		return "", false
	}
	assetDir := strings.TrimSuffix(gdDir.GetPath(), "/")
	return assetDir, assetDir != ""
}

func shouldReadConfigFromEngine(assetDir string) bool {
	schema, _ := spxfs.SplitSchema(assetDir)
	return schema != ""
}

// NormalizeConfigPath resolves a relative project config path from configDir.
func NormalizeConfigPath(configDir, relPath string) string {
	if relPath == "" {
		return ""
	}
	if strings.HasPrefix(relPath, "/") {
		return relPath
	}
	if schema, _ := spxfs.SplitSchema(relPath); schema != "" {
		return relPath
	}
	return path.Clean(path.Join(configDir, relPath))
}

func normalizeProjectConfigPaths(conf *ProjectConfig) {
	if conf == nil {
		return
	}

	for _, backdrop := range conf.Backdrops {
		if backdrop == nil {
			continue
		}
		backdrop.Path = NormalizeConfigPath("", backdrop.Path)
	}
	conf.Bgm = NormalizeConfigPath("", conf.Bgm)
	conf.TilemapPath = NormalizeConfigPath("", conf.TilemapPath)
}

func normalizeSpriteConfigPaths(conf *SpriteConfig, configDir string) {
	if conf == nil {
		return
	}

	for _, costume := range conf.Costumes {
		if costume == nil {
			continue
		}
		costume.Path = NormalizeConfigPath(configDir, costume.Path)
	}
	if conf.CostumeSet != nil {
		conf.CostumeSet.Path = NormalizeConfigPath(configDir, conf.CostumeSet.Path)
	}
	if conf.CostumeMPSet != nil {
		conf.CostumeMPSet.Path = NormalizeConfigPath(configDir, conf.CostumeMPSet.Path)
	}
}
