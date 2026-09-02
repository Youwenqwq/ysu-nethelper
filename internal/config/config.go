// Package config 加载 Daemon/CLI 的 JSON 配置（零依赖优先于 TOML，
// 嵌入式路由器场景下保持静态二进制最小）。
package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed template.json
var templateData []byte

// Duration 是 JSON 字符串形式的 time.Duration（如 "60s"、"5m"）。
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// D 返回 time.Duration 值。
func (d Duration) D() time.Duration { return time.Duration(d) }

// DaemonConfig 是 Daemon 模式的调参。
type DaemonConfig struct {
	// ProbeURLs Internet 204 探针列表；空则用内置默认。
	ProbeURLs []string `json:"probe_urls"`
	// ProbeInterval 在线时的探测间隔，默认 60s。
	ProbeInterval Duration `json:"probe_interval"`
	// ProbeConfirm 探针失败需连续确认的次数（防抖），默认 3。
	ProbeConfirm int `json:"probe_confirm"`
	// ProbeConfirmGap 确认探测之间的间隔，默认 3s。
	ProbeConfirmGap Duration `json:"probe_confirm_gap"`
	// ProbeTimeout 单次探针请求超时，默认 5s。
	ProbeTimeout Duration `json:"probe_timeout"`
	// NoLinkInterval portal 不可达（不在校园网）时的重试间隔，默认 15s。
	NoLinkInterval Duration `json:"nolink_interval"`
	// BackoffInitial 认证失败的初始退避，默认 10s。
	BackoffInitial Duration `json:"backoff_initial"`
	// BackoffMax 退避上限，默认 10m。
	BackoffMax Duration `json:"backoff_max"`
}

// Config 是顶层配置。
type Config struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Service 网络服务名，默认 "校园网"，支持别名 campus/unicom/telecom/mobile。
	Service string `json:"service"`
	// CredentialPath TGC 凭据持久化路径，默认 ~/.config/ysu-nethelper/cas.json。
	CredentialPath string `json:"credential_path"`
	// HTTPTimeout 认证类请求超时，默认 30s。
	HTTPTimeout Duration     `json:"http_timeout"`
	Daemon      DaemonConfig `json:"daemon"`

	path string // 配置文件自身路径，非序列化
}

// DefaultPath 返回默认配置路径。
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(home, ".config", "ysunethelper", "config.json")
}

// CWDConfigFilename 是当前目录配置文件名（优先于用户级默认路径）。
const CWDConfigFilename = "ysunethelper.json"

// SystemPath 是系统级配置路径（服务部署场景）。
const SystemPath = "/etc/ysunethelper/config.json"

// ResolveCLIPath 解析交互式 CLI 的配置路径：flagPath 显式指定 >
// 当前目录 ysunethelper.json > 用户级默认路径。
// 系统服务应使用 ResolvePath 或在服务单元中显式指定 SystemPath。
func ResolveCLIPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if _, err := os.Stat(CWDConfigFilename); err == nil {
		return CWDConfigFilename
	}
	return DefaultPath()
}

// ResolvePath 解析配置文件路径：flagPath 显式指定 > 当前目录
// ysunethelper.json > 用户级默认路径 > 系统级路径。
func ResolvePath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if _, err := os.Stat(CWDConfigFilename); err == nil {
		return CWDConfigFilename
	}
	if _, err := os.Stat(DefaultPath()); err == nil {
		return DefaultPath()
	}
	return SystemPath
}

// IsNotExist 报告加载失败是否因配置文件不存在。
func IsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// WriteTemplate 在 path 生成一份配置模板（0600），
// 用于 daemon 首次运行时的自动初始化。
func WriteTemplate(path string) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, templateData, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// DefaultCredentialPath 返回默认凭据路径。
func DefaultCredentialPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "cas.json"
	}
	return filepath.Join(home, ".config", "ysunethelper", "cas.json")
}

// ApplyDefaults 填充默认值。
func (c *Config) ApplyDefaults() {
	if c.Service == "" {
		c.Service = "校园网"
	}
	if c.CredentialPath == "" {
		c.CredentialPath = DefaultCredentialPath()
	}
	if c.HTTPTimeout == 0 {
		c.HTTPTimeout = Duration(30 * time.Second)
	}
	d := &c.Daemon
	if d.ProbeInterval == 0 {
		d.ProbeInterval = Duration(60 * time.Second)
	}
	if d.ProbeConfirm == 0 {
		d.ProbeConfirm = 3
	}
	if d.ProbeConfirmGap == 0 {
		d.ProbeConfirmGap = Duration(3 * time.Second)
	}
	if d.ProbeTimeout == 0 {
		d.ProbeTimeout = Duration(5 * time.Second)
	}
	if d.NoLinkInterval == 0 {
		d.NoLinkInterval = Duration(15 * time.Second)
	}
	if d.BackoffInitial == 0 {
		d.BackoffInitial = Duration(10 * time.Second)
	}
	if d.BackoffMax == 0 {
		d.BackoffMax = Duration(10 * time.Minute)
	}
}

// Validate 做最基本的一致性检查。service 为空时由 ApplyDefaults
// 设置为“校园网”，因此不是必填字段。
func (c *Config) Validate() error {
	var missing []string
	if strings.TrimSpace(c.Username) == "" {
		missing = append(missing, "username")
	}
	if c.Password == "" {
		missing = append(missing, "password")
	}
	if strings.TrimSpace(c.Service) == "" {
		missing = append(missing, "service")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: %s required", strings.Join(missing, ", "))
	}
	return nil
}

// Path 返回配置文件路径。
func (c *Config) Path() string { return c.path }

// Load 读取配置文件并填充默认值。
func Load(path string) (*Config, error) {
	if path == "" {
		path = ResolvePath("")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.path = path
	c.ApplyDefaults()
	// 含密码的配置文件权限过宽时告警（不强制，容器/Windows 下可能无法 chmod）
	if fi, err := os.Stat(path); err == nil && c.Password != "" && fi.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "warning: config file %s is readable by others (mode %o); recommend chmod 600\n",
			path, fi.Mode().Perm())
	}
	return &c, nil
}

// LoadOptional 与 Load 相同，但配置文件不存在时返回仅含默认值的配置。
// path 为空时使用 daemon 的默认查找顺序；交互式 CLI 使用 LoadOptionalCLI。
// 存在但解析失败仍报错。
func LoadOptional(path string) (*Config, error) {
	c, err := Load(path)
	if err != nil && IsNotExist(err) {
		c = &Config{path: ResolvePath(path)}
		c.ApplyDefaults()
		return c, nil
	}
	return c, err
}

// LoadOptionalCLI 与 LoadOptional 相同，但交互式 CLI 的隐式查找
// 不读取系统级配置，避免 0600 root:root 的 /etc 配置阻塞普通用户。
// 显式 -config 仍然严格按指定路径读取。
func LoadOptionalCLI(path string) (*Config, error) {
	if path != "" {
		return LoadOptional(path)
	}
	resolved := ResolveCLIPath("")
	c, err := Load(resolved)
	if err != nil && IsNotExist(err) {
		c = &Config{path: resolved}
		c.ApplyDefaults()
		return c, nil
	}
	return c, err
}
