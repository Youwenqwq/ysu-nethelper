package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvePathOrder 验证解析顺序：flag > CWD > ~/.config > /etc。
func TestResolvePathOrder(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	cwd := filepath.Join(tmp, "cwd")
	for _, d := range []string{home, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	// flag 显式指定优先
	if got := ResolvePath("/custom/x.json"); got != "/custom/x.json" {
		t.Errorf("flag path: got %q", got)
	}
	// 无 CWD 无 HOME 配置 → 系统级
	if got := ResolvePath(""); got != SystemPath {
		t.Errorf("no config: got %q, want %q", got, SystemPath)
	}
	// HOME 配置出现 → 用户级
	userPath := filepath.Join(home, ".config", "ysunethelper", "config.json")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolvePath(""); got != userPath {
		t.Errorf("user config: got %q, want %q", got, userPath)
	}
	// CWD 配置出现 → 最高优先
	cwdPath := filepath.Join(cwd, CWDConfigFilename)
	if err := os.WriteFile(cwdPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolvePath(""); got != CWDConfigFilename {
		t.Errorf("cwd config: got %q, want %q", got, CWDConfigFilename)
	}
}

// TestLoadOptionalMissing 无配置时返回纯默认值，不报错。
func TestLoadOptionalMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	c, err := LoadOptional(filepath.Join(tmp, "nonexistent.json"))
	if err != nil {
		t.Fatalf("LoadOptional: %v", err)
	}
	if c.Service != "校园网" || c.Daemon.ProbeConfirm == 0 {
		t.Errorf("defaults not applied: %+v", c)
	}
	// 无凭据时 Validate 必须失败（daemon 路径依赖此行为）
	if err := c.Validate(); err == nil {
		t.Error("Validate should fail without credentials")
	}
}

// TestLoadOptionalBroken 配置存在但损坏时必须报错（不静默忽略）。
func TestLoadOptionalBroken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOptional(p); err == nil {
		t.Fatal("broken config should error")
	}
}

// TestWriteTemplate 模板可解析且权限为 0600。
func TestWriteTemplate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", CWDConfigFilename)
	if err := WriteTemplate(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("template should parse: %v", err)
	}
}
