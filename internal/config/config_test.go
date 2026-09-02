package config

import (
	"os"
	"path/filepath"
	"strings"
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

// TestResolveCLIPathOrder 验证交互式 CLI 不回退到系统级配置。
func TestResolveCLIPathOrder(t *testing.T) {
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

	if got := ResolveCLIPath("/custom/x.json"); got != "/custom/x.json" {
		t.Errorf("flag path: got %q", got)
	}
	want := filepath.Join(home, ".config", "ysunethelper", "config.json")
	if got := ResolveCLIPath(""); got != want {
		t.Errorf("no config: got %q, want %q", got, want)
	}

	if err := os.WriteFile(filepath.Join(home, ".config", "ysunethelper", "config.json"), []byte("{}"), 0o600); err == nil {
		t.Fatal("expected missing parent directory error")
	}
	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveCLIPath(""); got != want {
		t.Errorf("user config: got %q, want %q", got, want)
	}

	cwdPath := filepath.Join(cwd, CWDConfigFilename)
	if err := os.WriteFile(cwdPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveCLIPath(""); got != CWDConfigFilename {
		t.Errorf("cwd config: got %q, want %q", got, CWDConfigFilename)
	}
}

// TestLoadOptionalCLIMissing 无用户配置时返回默认值且不尝试系统级配置。
func TestLoadOptionalCLIMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	c, err := LoadOptionalCLI("")
	if err != nil {
		t.Fatalf("LoadOptionalCLI: %v", err)
	}
	if got, want := c.Path(), filepath.Join(tmp, ".config", "ysunethelper", "config.json"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if c.Service != "校园网" || c.Daemon.ProbeConfirm == 0 {
		t.Errorf("defaults not applied: %+v", c)
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
	err = c.Validate()
	if err == nil {
		t.Error("Validate should fail without credentials")
	}
	if !strings.Contains(err.Error(), "username") || !strings.Contains(err.Error(), "password") {
		t.Errorf("Validate error should name all missing fields: %v", err)
	}
}

// TestValidateBlankService 空白 service 不能绕过配置校验。
func TestValidateBlankService(t *testing.T) {
	c := &Config{Username: "user", Password: "password", Service: " \t"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "service") {
		t.Errorf("Validate should reject blank service, got %v", err)
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
