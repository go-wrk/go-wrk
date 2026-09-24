package main

// 压测配置的保存与加载。两套用法并存：
//
//   - 命名配置列表，界面上随时切换 —— 存在 %APPDATA%\go-wrk-desktop\configs.json
//   - 导出 / 导入单个 .json 文件 —— 走系统文件对话框，方便发给别人或放进版本管理
//
// 写列表前会重读一遍文件。多开时两个实例各存各的配置，不重读的话
// 后写的那个会把先写的那条抹掉。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	appDirName  = "go-wrk-desktop"
	configsFile = "configs.json"
	maxNameLen  = 60
)

// 配置文件的读写锁。SaveConfig 和 DeleteConfig 都是「读文件 → 改 → 写回」，
// 不串行化的话并发的两次操作会互相覆盖 —— 实测 6 个并发保存，落盘只剩 1 条。
var configsMu sync.Mutex

// savedAtLayout 存配置时刻用的格式。
//
// 必须是定长的：time.RFC3339Nano 会去掉小数末尾的零，字典序就不等于时间序了
// （".5Z" 按字符串比反而大于 ".55Z"，但时间上前者更早），而列表正是按字符串
// 倒序排的。定长格式下纳秒位永远是九位，两个顺序才一致。
const savedAtLayout = "2006-01-02T15:04:05.000000000Z07:00"

// NamedConfig 一条存下来的配置。
type NamedConfig struct {
	Name    string `json:"name"`
	SavedAt string `json:"savedAt"` // 格式见 savedAtLayout
	Config  Config `json:"config"`
}

// dataDir 应用的本地数据目录：放配置，也给 WebView2 存缓存。
func dataDir() (string, error) {
	base := os.Getenv("APPDATA")
	if base == "" {
		// 非 Windows 或环境变量缺失时退回用户主目录
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("找不到能写入的目录：%v", err)
		}
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, appDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建数据目录失败：%v", err)
	}
	return dir, nil
}

func readConfigs() ([]NamedConfig, error) {
	dir, err := dataDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, configsFile))
	if errors.Is(err, os.ErrNotExist) {
		return []NamedConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置失败：%v", err)
	}
	var list []NamedConfig
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("配置文件格式不对：%v", err)
	}
	return list, nil
}

func writeConfigs(list []NamedConfig) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再改名：写到一半崩了也不会留下半个坏文件。
	// 临时名带上进程号 —— 多开时两个实例各写各的，不会互相截断
	tmp := filepath.Join(dir, fmt.Sprintf("%s.%d.tmp", configsFile, os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入配置失败：%v", err)
	}
	return os.Rename(tmp, filepath.Join(dir, configsFile))
}

// ListConfigs 列出所有已保存的配置，最近保存的排前面。
func (a *App) ListConfigs() ([]NamedConfig, error) {
	list, err := readConfigs()
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].SavedAt > list[j].SavedAt })
	return list, nil
}

// SaveConfig 按名字保存，同名覆盖，返回更新后的列表。
func (a *App) SaveConfig(name string, cfg Config) ([]NamedConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("请先填写配置名")
	}
	if len([]rune(name)) > maxNameLen {
		return nil, fmt.Errorf("配置名太长了，%d 个字以内", maxNameLen)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}

	// 校验在锁外做掉，只有真正的读—改—写需要串行
	configsMu.Lock()
	defer configsMu.Unlock()

	list, err := readConfigs()
	if err != nil {
		return nil, err
	}

	entry := NamedConfig{Name: name, SavedAt: time.Now().UTC().Format(savedAtLayout), Config: cfg}
	replaced := false
	for i := range list {
		if list[i].Name == name {
			list[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, entry)
	}

	if err := writeConfigs(list); err != nil {
		return nil, err
	}
	return a.ListConfigs()
}

// DeleteConfig 删掉一条配置，返回更新后的列表。
func (a *App) DeleteConfig(name string) ([]NamedConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("请先选择要删除的配置")
	}

	configsMu.Lock()
	defer configsMu.Unlock()

	list, err := readConfigs()
	if err != nil {
		return nil, err
	}

	kept := make([]NamedConfig, 0, len(list))
	found := false
	for _, c := range list {
		if c.Name == name {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		return nil, fmt.Errorf("没有叫「%s」的配置", name)
	}

	if err := writeConfigs(kept); err != nil {
		return nil, err
	}
	return a.ListConfigs()
}

// ExportConfig 弹保存对话框，把当前配置写成一个 .json 文件。
// 返回写到的路径；用户取消时返回空串。
func (a *App) ExportConfig(cfg Config) (string, error) {
	// Wails 的运行时函数在 context 里找不到前端时会 log.Fatalf 直接退进程，
	// 所以这里必须自己先拦一道，别把空 context 递进去
	base := a.ctx.Load()
	if base == nil {
		return "", errors.New("界面还没准备好，稍等一下再试")
	}

	path, err := runtime.SaveFileDialog(*base, runtime.SaveDialogOptions{
		Title:           "导出压测配置",
		DefaultFilename: "go-wrk-config.json",
		Filters: []runtime.FileFilter{
			{DisplayName: "JSON 配置 (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("写入文件失败：%v", err)
	}
	return path, nil
}

// ImportConfig 弹打开对话框，从一个 .json 文件读一份配置。
// 用户取消时返回 URL 为空的配置，前端据此判断。
func (a *App) ImportConfig() (Config, error) {
	base := a.ctx.Load()
	if base == nil {
		return Config{}, errors.New("界面还没准备好，稍等一下再试")
	}

	path, err := runtime.OpenFileDialog(*base, runtime.OpenDialogOptions{
		Title: "打开压测配置",
		Filters: []runtime.FileFilter{
			{DisplayName: "JSON 配置 (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return Config{}, err
	}
	if path == "" {
		return Config{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取文件失败：%v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("不是有效的压测配置文件：%v", err)
	}
	if err := cfg.normalize(); err != nil {
		return Config{}, fmt.Errorf("配置文件里的参数不对：%v", err)
	}
	return cfg, nil
}
