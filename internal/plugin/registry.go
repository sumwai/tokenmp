package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// 本文件是中间件的本机注册表：注册了哪些插件、哪些启用、按什么顺序执行。
//
// 清单放在本机文件而不是数据库：插件路径是运行机的事实，多实例共享一个库时，
// 一台机器上注册的路径在另一台上并不存在，把它当共享配置会让另一台实例启动失败。
// 清单只由 `admin plugin` 写，`serve` 启动时读一次。

// 清单文件与它所在目录的权限：只给属主读写，不共享给其他用户。
const (
	registryFileMode = 0o600
	registryDirMode  = 0o750
)

// Entry 是注册表里的一项。
type Entry struct {
	// Name 是插件的句柄，enable / disable / del 按它定位；默认取入口文件名或包目录名。
	Name string `json:"name"`
	// Path 是入口文件或包目录的绝对路径。
	Path string `json:"path"`
	// Enabled 为假时 serve 不加载它，但注册与顺序都保留。
	Enabled bool `json:"enabled"`
}

// Registry 是本机的插件清单，顺序即装配顺序。
type Registry struct {
	Plugins []Entry `json:"plugins"`
}

// LoadRegistry 读清单；文件不存在返回空清单（等同没注册任何插件）。
//
// 形状非法（键名不认识、名字或路径为空、名字重复）按配置错误报出：清单由本机命令写入，
// 读不出来就是有人手改坏了，静默忽略会让插件无声地不生效。
func LoadRegistry(path string) (*Registry, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304：路径来自本机配置，不是请求输入。
	if errors.Is(err, os.ErrNotExist) {
		return &Registry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取插件清单 %s 失败：%w", path, err)
	}
	var registry Registry
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return nil, fmt.Errorf("插件清单 %s 不是合法 JSON：%w", path, err)
	}
	if err := registry.validate(); err != nil {
		return nil, fmt.Errorf("插件清单 %s 不可用：%w", path, err)
	}
	return &registry, nil
}

// validate 检查清单的形状：名字与路径非空、名字不重复。
func (r *Registry) validate() error {
	seen := make(map[string]bool, len(r.Plugins))
	for i, entry := range r.Plugins {
		if entry.Name == "" {
			return fmt.Errorf("第 %d 项缺少名字", i+1)
		}
		if entry.Path == "" {
			return fmt.Errorf("第 %d 项 %s 缺少路径", i+1, entry.Name)
		}
		if seen[entry.Name] {
			return fmt.Errorf("名字 %s 重复", entry.Name)
		}
		seen[entry.Name] = true
	}
	return nil
}

// SaveRegistry 原子写入清单：先写同目录临时文件再改名，避免 serve 读到半份文件。
func SaveRegistry(path string, registry *Registry) error {
	if err := registry.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), registryDirMode); err != nil {
		return fmt.Errorf("创建清单目录失败：%w", err)
	}
	encoded, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("编码插件清单失败：%w", err)
	}
	encoded = append(encoded, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".plugins-*.json")
	if err != nil {
		return fmt.Errorf("创建临时清单失败：%w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return fmt.Errorf("写临时清单失败：%w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("关闭临时清单失败：%w", err)
	}
	if err := os.Chmod(tempName, registryFileMode); err != nil {
		return fmt.Errorf("设置清单权限失败：%w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("替换插件清单失败：%w", err)
	}
	return nil
}

// Find 按名字找一项。
func (r *Registry) Find(name string) (Entry, bool) {
	for _, entry := range r.Plugins {
		if entry.Name == name {
			return entry, true
		}
	}
	return Entry{}, false
}

// Add 追加一项；名字或路径与已有项重复时报错。
//
// 重复路径单独判：同一个文件注册两次会让它的钩子被调用两遍，而钩子是按顺序累积改写的。
func (r *Registry) Add(entry Entry) error {
	if entry.Name == "" || entry.Path == "" {
		return errors.New("插件条目需要名字与路径")
	}
	if _, exists := r.Find(entry.Name); exists {
		return fmt.Errorf("名字 %s 已被占用", entry.Name)
	}
	for _, existing := range r.Plugins {
		if existing.Path == entry.Path {
			return fmt.Errorf("路径 %s 已注册为 %s", entry.Path, existing.Name)
		}
	}
	r.Plugins = append(r.Plugins, entry)
	return nil
}

// SetEnabled 按名字改启用状态。
func (r *Registry) SetEnabled(name string, enabled bool) error {
	for i := range r.Plugins {
		if r.Plugins[i].Name == name {
			r.Plugins[i].Enabled = enabled
			return nil
		}
	}
	return fmt.Errorf("没有名为 %s 的插件", name)
}

// Remove 按名字删除一项；删除只影响清单，不动插件文件。
func (r *Registry) Remove(name string) error {
	for i, entry := range r.Plugins {
		if entry.Name == name {
			r.Plugins = append(r.Plugins[:i], r.Plugins[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("没有名为 %s 的插件", name)
}

// EnabledPaths 返回启用项入口路径，顺序与清单一致。
//
// 顺序就是装配顺序：多个插件的同名钩子按注册顺序依次改写，先后不同结果不同。
func (r *Registry) EnabledPaths() []string {
	paths := make([]string, 0, len(r.Plugins))
	for _, entry := range r.Plugins {
		if entry.Enabled {
			paths = append(paths, entry.Path)
		}
	}
	return paths
}
