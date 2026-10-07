package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Calcium-Ion/moejs"
)

// 中间件文件的命名约定与包声明文件。
const (
	// fileSuffixJS 与 fileSuffixMJS 是直接以文件形式配置中间件时要求的扩展名。
	// 两个后缀并存是为了区分「中间件」与同目录下的普通脚本文件。
	fileSuffixJS  = ".mw.js"
	fileSuffixMJS = ".mw.mjs"
	// packageFile 是包目录形式的入口声明文件。
	packageFile = "package.json"
	// packageEntryField 是包目录声明中间件入口的字段名；
	// 未声明时回退到 npm 约定的 main 字段。
	packageEntryField = "moejs"
	// packageMainField 是包目录的回退入口字段。
	packageMainField = "main"
)

// fileStamp 是判断文件是否变化的指纹：修改时间纳秒与大小。
//
// 用「修改时间 + 大小」而不是内容哈希：热重载检查发生在每次取用中间件时，
// 读文件内容会把热路径变成长路径；指纹只回答「值不值得重新编译」。
type fileStamp struct {
	modTime int64
	size    int64
}

// statFile 读取文件指纹。
func statFile(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{modTime: info.ModTime().UnixNano(), size: info.Size()}, nil
}

// cacheEntry 是一个已编译模块及其指纹。
type cacheEntry struct {
	module *moejs.Module
	stamp  fileStamp
}

// compileSession 累计一次图编译触及的文件路径，供调用方登记依赖。
//
// 动态导入不参与这里的依赖登记：它的解析时机由插件代码决定，不属启动期已知的模块图。
// 这类文件由 noteRuntimeDep 单独登记，同样进入热重载的指纹判定。
type compileSession struct {
	deps []string
}

// add 登记一个被触及的文件路径。
func (s *compileSession) add(path string) {
	if s == nil {
		return
	}
	s.deps = append(s.deps, path)
}

// compiler 按文件路径缓存已编译模块，并把模块图限制在插件目录内。
//
// 缓存以指纹为准：指纹未变时复用已编译模块，变了才重新解析编译。
// 一个中间件一个 compiler：沙箱根目录由中间件所在目录决定，跨中间件共享缓存
// 会让「目录内」这条边界随中间件归属漂移。
//
// 缓存会被两类调用并发触及：启动期与热重载的图编译，以及运行时里动态 import()
// 触发的按需解析；因此每一次读写都在同一把锁下完成。
type compiler struct {
	// root 是沙箱根目录：模块导入不得解析到该目录之外。
	root string
	// noteRuntimeDep 记录一次运行期解析（动态 import）触及的文件与当时的指纹，
	// 供热重载判定。为 nil 时不记录。
	noteRuntimeDep func(path string, stamp fileStamp)

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// newCompiler 构造一个以 root 为边界的编译器；root 会被转为绝对路径。
//
// noteRuntimeDep 可以为 nil；它只会在运行期解析路径上被调用。
func newCompiler(root string, noteRuntimeDep func(path string, stamp fileStamp)) (*compiler, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析插件目录失败：%w", err)
	}
	return &compiler{root: abs, cache: map[string]cacheEntry{}, noteRuntimeDep: noteRuntimeDep}, nil
}

// compileGraph 编译入口模块并链接它的全部静态导入，返回链接后的模块、
// 触及的文件路径与各自的指纹。
//
// 链接后的模块才是运行时可加载的对象：有导入的模块必须经 moejs.Link 把整张图
// 一次链接，运行时只负责实例化与求值。
func (c *compiler) compileGraph(entry string) (*moejs.Module, []string, map[string]fileStamp, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session := &compileSession{}
	entryModule, err := c.compileFile(entry, session)
	if err != nil {
		return nil, nil, nil, err
	}
	linked := entryModule
	if len(entryModule.Requests()) > 0 {
		linked, err = moejs.Link(entryModule, func(referrer moejs.Referrer, specifier string) (*moejs.Module, error) {
			return c.resolveLocked(referrer, specifier, session)
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("链接插件 %s 失败：%w", entry, err)
		}
	}
	deps := append([]string(nil), session.deps...)
	stamps := make(map[string]fileStamp, len(deps))
	for _, dep := range deps {
		stamp, err := statFile(dep)
		if err != nil {
			return nil, nil, nil, err
		}
		stamps[dep] = stamp
	}
	return linked, deps, stamps, nil
}

// compileFile 按指纹复用或重新编译一个模块文件。调用方必须已持有 c.mu。
//
// session 为 nil 表示这次编译来自运行期解析（动态 import）：这类文件不在启动期的
// 静态模块图里，要把路径与指纹回传给调用方，否则改它不会触发重载，也不会清掉池里
// 已经装载旧模块的运行时。
func (c *compiler) compileFile(path string, session *compileSession) (*moejs.Module, error) {
	stamp, err := statFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取插件文件 %s 失败：%w", path, err)
	}
	if session == nil && c.noteRuntimeDep != nil {
		c.noteRuntimeDep(path, stamp)
	}
	if cached, ok := c.cache[path]; ok && cached.stamp == stamp {
		session.add(path)
		return cached.module, nil
	}
	source, err := os.ReadFile(path) //nolint:gosec // G304：路径来自本机插件配置，不是请求输入。
	if err != nil {
		return nil, fmt.Errorf("读取插件文件 %s 失败：%w", path, err)
	}
	module, err := moejs.Compile(path, string(source))
	if err != nil {
		return nil, fmt.Errorf("编译插件 %s 失败：%w", path, err)
	}
	c.cache[path] = cacheEntry{module: module, stamp: stamp}
	session.add(path)
	return module, nil
}

// resolve 是运行时动态导入走的入口：取锁后转交 resolve。
func (c *compiler) resolve(referrer moejs.Referrer, specifier string) (*moejs.Module, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolveLocked(referrer, specifier, nil)
}

// resolveLocked 是 moejs 侧的模块解析器，同时是导入沙箱：只接受相对说明符，
// 且解析结果必须落在插件目录内。调用方必须已持有 c.mu。
//
// 裸说明符（node_modules 包名与 Node 内置模块名）一律拒绝：插件须自带依赖，
// 引擎也不提供任何 Node API，放行裸说明符只会得到一个延迟到运行时的失败。
func (c *compiler) resolveLocked(referrer moejs.Referrer, specifier string, session *compileSession) (*moejs.Module, error) {
	if !isRelativeSpecifier(specifier) {
		return nil, fmt.Errorf("插件只允许目录内的相对导入，拒绝 %q", specifier)
	}
	base := filepath.Dir(referrer.Name())
	candidate := filepath.Clean(filepath.Join(base, specifier))
	if !c.insideRoot(candidate) {
		return nil, fmt.Errorf("导入 %q 解析到插件目录之外", specifier)
	}
	resolved, err := resolveModulePath(candidate)
	if err != nil {
		return nil, fmt.Errorf("导入 %q 无法解析：%w", specifier, err)
	}
	return c.compileFile(resolved, session)
}

// insideRoot 报告绝对路径是否位于沙箱根目录内。
func (c *compiler) insideRoot(path string) bool {
	rel, err := filepath.Rel(c.root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isRelativeSpecifier 报告导入说明符是否为相对路径形态。
func isRelativeSpecifier(specifier string) bool {
	return strings.HasPrefix(specifier, "./") || strings.HasPrefix(specifier, "../")
}

// moduleExtensions 是导入说明符未带扩展名时依次尝试的后缀。
var moduleExtensions = []string{".js", ".mjs"}

// resolveModulePath 把导入路径解析到实际文件：已有文件直接用，
// 否则依次补常见扩展名与目录 index。
func resolveModulePath(candidate string) (string, error) {
	if isRegularFile(candidate) {
		return candidate, nil
	}
	for _, ext := range moduleExtensions {
		if isRegularFile(candidate + ext) {
			return candidate + ext, nil
		}
	}
	for _, ext := range moduleExtensions {
		index := filepath.Join(candidate, "index"+ext)
		if isRegularFile(index) {
			return index, nil
		}
	}
	return "", fmt.Errorf("没有匹配 %s 的文件", filepath.Base(candidate))
}

// isRegularFile 报告路径存在且是普通文件。
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// resolveEntry 把配置里的一项解析为入口文件、沙箱根目录与展示用名称。
//
// 两种形态：文件名直接给出入口，要求中间件后缀；目录形态从 package.json 读入口，
// 目录本身即沙箱根。其余形态在启动期报错，不推迟到第一个请求。
func resolveEntry(path string) (entry, root, name string, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", "", fmt.Errorf("解析插件路径 %s 失败：%w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", "", fmt.Errorf("插件路径 %s 不可用：%w", path, err)
	}
	if info.IsDir() {
		declared, entryErr := entryFromPackage(abs)
		if entryErr != nil {
			return "", "", "", entryErr
		}
		return declared, abs, filepath.Base(abs), nil
	}
	base := filepath.Base(abs)
	if !hasMiddlewareSuffix(base) {
		return "", "", "", fmt.Errorf("插件文件 %s 须以 %s 或 %s 结尾", path, fileSuffixJS, fileSuffixMJS)
	}
	return abs, filepath.Dir(abs), base, nil
}

// hasMiddlewareSuffix 报告文件名是否以中间件后缀结尾。
func hasMiddlewareSuffix(name string) bool {
	return strings.HasSuffix(name, fileSuffixJS) || strings.HasSuffix(name, fileSuffixMJS)
}

// entryFromPackage 读取包目录里的入口声明。
func entryFromPackage(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, packageFile)) //nolint:gosec // G304：目录来自本机插件配置，不是请求输入。
	if err != nil {
		return "", fmt.Errorf("目录 %s 缺少 %s：%w", dir, packageFile, err)
	}
	var manifest struct {
		Moejs string `json:"moejs"`
		Main  string `json:"main"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", fmt.Errorf("%s 不是合法 JSON：%w", filepath.Join(dir, packageFile), err)
	}
	declared := strings.TrimSpace(manifest.Moejs)
	if declared == "" {
		declared = strings.TrimSpace(manifest.Main)
	}
	if declared == "" {
		return "", fmt.Errorf("%s 未声明 %q 或 %q 入口", packageFile, packageEntryField, packageMainField)
	}
	resolved := filepath.Join(dir, filepath.FromSlash(declared))
	if !isRegularFile(resolved) {
		return "", fmt.Errorf("%s 声明的入口 %s 不存在", packageFile, declared)
	}
	return resolved, nil
}
