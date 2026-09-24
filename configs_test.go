package main

// 配置存取的测试。每个用例把 APPDATA 指到临时目录，
// 免得测试碰到用户真实的配置。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("APPDATA", t.TempDir())
	return NewApp()
}

func TestSaveAndListConfigs(t *testing.T) {
	app := testApp(t)

	if list, err := app.ListConfigs(); err != nil || len(list) != 0 {
		t.Fatalf("新目录该是空的，实际 %d 条（err=%v）", len(list), err)
	}

	cfg := Config{URL: "https://example.com/api", Method: "POST", Concurrency: 50, Duration: 30, Timeout: 2000}
	list, err := app.SaveConfig("我的API", cfg)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if len(list) != 1 || list[0].Name != "我的API" {
		t.Fatalf("保存后列表不对: %+v", list)
	}
	if list[0].Config.Concurrency != 50 || list[0].Config.Method != "POST" {
		t.Errorf("配置没存全: %+v", list[0].Config)
	}
	if list[0].SavedAt == "" {
		t.Error("没记保存时间")
	}

	// 同名覆盖，不该多出一条
	cfg.Concurrency = 100
	list, err = app.SaveConfig("我的API", cfg)
	if err != nil {
		t.Fatalf("覆盖保存失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("同名保存该覆盖，实际 %d 条", len(list))
	}
	if list[0].Config.Concurrency != 100 {
		t.Errorf("覆盖后并发数 = %d，期望 100", list[0].Config.Concurrency)
	}
}

func TestDeleteConfig(t *testing.T) {
	app := testApp(t)
	cfg := Config{URL: "https://example.com"}

	if _, err := app.SaveConfig("a", cfg); err != nil {
		t.Fatal(err)
	}
	list, err := app.SaveConfig("b", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("该有 2 条，实际 %d", len(list))
	}

	list, err = app.DeleteConfig("a")
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if len(list) != 1 || list[0].Name != "b" {
		t.Fatalf("删完剩下: %+v", list)
	}

	if _, err := app.DeleteConfig("不存在"); err == nil {
		t.Error("删不存在的配置该报错")
	}
}

func TestSaveConfigValidation(t *testing.T) {
	app := testApp(t)

	if _, err := app.SaveConfig("   ", Config{URL: "https://example.com"}); err == nil {
		t.Error("空名字该被拒")
	}
	if _, err := app.SaveConfig("x", Config{URL: "不是地址"}); err == nil {
		t.Error("无效地址该被拒")
	}
}

// 保存时补上的默认值要落盘，这样加载回来就是一份完整配置。
func TestSavedConfigKeepsDefaults(t *testing.T) {
	app := testApp(t)

	list, err := app.SaveConfig("默认值", Config{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	got := list[0].Config
	if got.Concurrency != 3 || got.Duration != 3 || got.Timeout != 3000 || got.Method != "GET" {
		t.Errorf("保存时没补默认值: %+v", got)
	}
}

// 模拟另一个实例刚写了一条 —— 我们的保存不能把它抹掉。
func TestSaveKeepsEntriesFromOtherInstances(t *testing.T) {
	app := testApp(t)

	if _, err := app.SaveConfig("先来的", Config{URL: "https://a.example.com"}); err != nil {
		t.Fatal(err)
	}

	dir, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, configsFile)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list []NamedConfig
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	list = append(list, NamedConfig{
		Name:    "别的实例写的",
		SavedAt: "2099-01-01T00:00:00Z",
		Config:  Config{URL: "https://b.example.com"},
	})
	data, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	after, err := app.SaveConfig("后来的", Config{URL: "https://c.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 3 {
		names := make([]string, len(after))
		for i, c := range after {
			names[i] = c.Name
		}
		t.Errorf("该有 3 条，实际 %d 条: %v", len(after), names)
	}
}

// 最近保存的排前面。
func TestListSortsBySavedAtDesc(t *testing.T) {
	app := testApp(t)

	if _, err := app.SaveConfig("旧", Config{URL: "https://old.example.com"}); err != nil {
		t.Fatal(err)
	}
	list, err := app.SaveConfig("新", Config{URL: "https://new.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "新" {
		t.Errorf("排序不对，第一条是 %q", list[0].Name)
	}
}

// 配置文件坏了要报错，不能静默当成空列表 —— 否则下次保存就把用户的配置覆盖没了。
func TestCorruptConfigFileReportsError(t *testing.T) {
	app := testApp(t)

	dir, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configsFile), []byte("{这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := app.ListConfigs(); err == nil {
		t.Error("配置文件损坏时该报错")
	}
}

// 保存是「读文件 → 改 → 写回」，不串行化的话并发保存会互相覆盖。
// 加锁之前实测过：6 个并发保存，落盘只剩 1 条。
func TestConcurrentSavesDoNotLoseEntries(t *testing.T) {
	app := testApp(t)

	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = app.SaveConfig(fmt.Sprintf("cfg-%d", idx), Config{URL: "https://example.com"})
		}(i)
	}
	wg.Wait()

	list, err := app.ListConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != n {
		names := make([]string, len(list))
		for i, c := range list {
			names[i] = c.Name
		}
		t.Errorf("并发保存该留下 %d 条，实际 %d 条: %v", n, len(list), names)
	}
}
