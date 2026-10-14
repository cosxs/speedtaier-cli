package cli

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// version 可在构建时注入，如 scripts/build.sh 使用的
// -ldflags "-X github.com/cosxs/speedtaier-cli/internal/cli.version=v0.6.0"。
// 未注入时从构建信息读取：go install 模块@版本 会写入模块版本，在 git 仓库内构建会写入提交信息。
var version string

type buildInfo struct {
	Version  string
	Commit   string
	Time     string
	Modified bool
}

func readBuildInfo() buildInfo {
	b := buildInfo{Version: version}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; b.Version == "" && v != "" && v != "(devel)" {
			b.Version = v
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value[:min(12, len(s.Value))]
			case "vcs.time":
				b.Time = s.Value
			case "vcs.modified":
				b.Modified = s.Value == "true"
			}
		}
	}
	if b.Version == "" {
		b.Version = "dev"
	}
	return b
}

// String 返回完整版本信息，如 "speedtaier v0.6.0 (commit 1a2b3c4d5e6f, 2026-10-14T08:00:00Z, go1.27.1, darwin/arm64)"。
func (b buildInfo) String() string {
	var parts []string
	if b.Commit != "" {
		commit := "commit " + b.Commit
		if b.Modified {
			commit += "-dirty"
		}
		parts = append(parts, commit)
	}
	if b.Time != "" {
		parts = append(parts, b.Time)
	}
	parts = append(parts, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
	return "speedtaier " + b.Version + " (" + strings.Join(parts, ", ") + ")"
}
