package main

import (
	"runtime/debug"
)

// version 由发布流程通过 -ldflags "-X main.version=vX.Y.Z" 注入 git tag；
// 本地 go build 保持 dev，versionString 回退到 Go 构建信息
// （go install 时的模块版本，或仓库构建时的 VCS 提交号）。
var version = "dev"

func versionString() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	var rev, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return "dev (" + rev + ")"
}
