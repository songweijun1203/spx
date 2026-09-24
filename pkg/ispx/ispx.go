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
	"fmt"
	"io/fs"
	"sync"

	"github.com/goplus/ixgo"
	"github.com/goplus/ixgo/transform"
	"github.com/goplus/ixgo/xgobuild"
	"github.com/goplus/mod/modfile"
	_ "github.com/goplus/spx/v3"
	spxfs "github.com/goplus/spx/v3/fs"
	"github.com/goplus/spx/v3/internal/engine"
	"github.com/goplus/spx/v3/pkg/ispx/internal/memfs"
)

func init() {
	// NOTE: Keep in sync with the config in spx's gox.mod.
	xgobuild.RegisterProject(&modfile.Project{
		Ext:      ".spx",
		Class:    "Game",
		Works:    []*modfile.Class{{Ext: ".spx", Class: "SpriteImpl", Embedded: true}},
		PkgPaths: []string{"github.com/goplus/spx/v3", "math"},
	})
}

var (
	mu          sync.Mutex
	ixgoCtx     *ixgo.Context
	ixgoInterp  *ixgo.Interp
	runDone     chan struct{}
	optimizeSSA bool
)

// initOptions controls optional interpreter initialization behavior.
type initOptions struct {
	optimizeSSA bool
}

// InitOption configures interpreter initialization.
type InitOption func(*initOptions)

// WithSSAOptimization enables ixgo's default SSA transformation pipeline before
// creating the interpreter.
func WithSSAOptimization() InitOption {
	return func(options *initOptions) {
		options.optimizeSSA = true
	}
}

// defaultPackagesToImport is the list of packages that are always imported by ispx.
var defaultPackagesToImport = []string{
	"fmt",
	"io",
	"io/fs",
	"math",
	"os",
	"reflect",
	"strconv",
	"strings",
	"sync",
	"sync/atomic",
	"time",
	"github.com/goplus/spx/v3",
	"github.com/qiniu/x/osx",
	"github.com/qiniu/x/stringslice",
	"github.com/qiniu/x/stringutil",
	"github.com/qiniu/x/xgo",
	"github.com/qiniu/x/xgo/ng",
}

// Init initializes the interpreter with the given ctx, which must not be
// modified after used. It can only be called once.
//
// If ctx is nil, a default [ixgo.Context] will be created.
//
// If ctx.Lookup is nil, a default lookup function will be set.
func Init(ctx *ixgo.Context, options ...InitOption) error {
	mu.Lock()
	defer mu.Unlock()

	if ixgoCtx != nil {
		panic("ispx: already initialized")
	}

	var opts initOptions
	for _, option := range options {
		option(&opts)
	}

	if ctx == nil {
		ctx = ixgo.NewContext(ixgo.SupportMultipleInterp | ixgo.EnableCachedReg | xgobuild.StaticLoad)
	}
	if ctx.Lookup == nil {
		ctx.Lookup = defaultIXGoContextLookup
	}
	ctx.SetPanic(logRuntimePanic)

	for _, pkg := range defaultPackagesToImport {
		ctx.Loader.Import(pkg)
	}

	// Register patch for spx to support functions with generic type like [spx.XGot_Game_XGox_GetWidget].
	//
	// See https://github.com/goplus/builder/issues/765#issuecomment-2313915805.
	if err := ctx.RegisterPatch("github.com/goplus/spx/v3", `
package spx

import . "github.com/goplus/spx/v3"

func XGot_Game_XGox_GetWidget[T any](sg ShapeGetter, name WidgetName) *T {
	widget, ok := GetWidget(sg, name).(any).(*T)
	if !ok {
		panic("GetWidget: type mismatch - " + name)
	}
	return widget
}
`); err != nil {
		return fmt.Errorf("failed to register spx patch: %w", err)
	}

	ixgoCtx = ctx
	optimizeSSA = opts.optimizeSSA
	return nil
}

// Build 把 .spx/.json 文件编译进解释器。
// 最近调用方：Web 的 ispxBuild()；最顶层入口：GameApp.InitGame() 或 Worker tryRunGoWasm()。
func Build(files map[string][]byte) error {
	return BuildFS(memfs.New(files))
}

// ConfigureFilesystemRoots sets strict project and asset roots before Init.
func ConfigureFilesystemRoots(projectDir, assetDir string) error {
	return configureFilesystemRoots(projectDir, assetDir, false)
}

// ConfigureLegacyFilesystemRoots retains bounded external asset references
// used by existing interpreted and native commands.
func ConfigureLegacyFilesystemRoots(projectDir, assetDir string) error {
	return configureFilesystemRoots(projectDir, assetDir, true)
}

func configureFilesystemRoots(projectDir, assetDir string, legacy bool) error {
	mu.Lock()
	defer mu.Unlock()
	if ixgoCtx != nil {
		return fmt.Errorf("ispx: filesystem roots must be configured before Init")
	}
	if legacy {
		return engine.SetLegacyFilesystemRoots(projectDir, assetDir)
	}
	return engine.SetFilesystemRoots(projectDir, assetDir)
}

// BuildFS 从借用的文件系统编译项目；引擎加载项目资源期间，该文件系统必须保持可用。
// 最近调用方：Build()；最顶层入口：JavaScript ispx_build()。
func BuildFS(fsys fs.FS) error {
	// 如果上一局仍在运行，先请求退出再替换解释器。
	if err := Shutdown(); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	if ixgoCtx == nil {
		panic("ispx: not initialized")
	}

	// 释放上一份解释器资源。
	if ixgoInterp != nil {
		ixgoInterp.UnsafeRelease()
		ixgoInterp = nil
	}

	source, err := xgobuild.BuildFSDir(ixgoCtx, newXGoParserFS(fsys), ".")
	if err != nil {
		return fmt.Errorf("failed to build XGo source: %w", err)
	}

	pkg, err := ixgoCtx.LoadFile("main.go", source)
	if err != nil {
		return fmt.Errorf("failed to load XGo source: %w", err)
	}
	if optimizeSSA {
		if err := transform.Transform(pkg); err != nil {
			return fmt.Errorf("failed to transform SSA: %w", err)
		}
	}

	interp, err := ixgoCtx.NewInterp(pkg)
	if err != nil {
		return fmt.Errorf("failed to create interp: %w", err)
	}

	// 项目资源会由 Engine 回调延迟加载，所以只有所有可能失败的编译步骤都成功后，
	// 才发布新的资源 schema；失败的重编译不能覆盖上一份可用资源源。
	spxfs.RegisterSchema("", func(path string) (spxfs.Dir, error) {
		return newSpxDir(fsys, path), nil
	})
	ixgoInterp = interp
	return nil
}

// Run 执行已经编译好的游戏 main.go，并阻塞到解释器退出；返回后必须重新 Build 才能再运行。
// 最近调用方：ispxStart() 启动的 goroutine；最顶层入口：GameApp.StartGame()。
func Run() (exitCode int, err error) {
	mu.Lock()
	if ixgoInterp == nil {
		mu.Unlock()
		panic("ispx: not built")
	}
	if runDone != nil {
		mu.Unlock()
		panic("ispx: already running")
	}
	ctx, interp := ixgoCtx, ixgoInterp
	runDone = make(chan struct{})
	mu.Unlock()

	defer func() {
		mu.Lock()
		close(runDone)
		runDone = nil
		mu.Unlock()
	}()

	return ctx.RunInterp(interp, "main.go", nil)
}

// Shutdown 请求当前游戏停止并等待退出。
// 最近调用方：ispxStop() 或下一次 BuildFS()；最顶层入口：GameApp.StopGame()/InitGame()。
func Shutdown() error {
	mu.Lock()

	// 游戏运行中时发出退出请求并等待 Run() 返回。
	for runDone != nil {
		done := runDone
		mu.Unlock()

		engine.RequestExit(0)
		<-done

		mu.Lock()
	}

	mu.Unlock()
	return nil
}
