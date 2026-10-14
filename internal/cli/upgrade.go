package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cosxs/speedtaier-cli/internal/update"
)

// noUpdateCheckEnv 设置为任意非空值时不检查新版本。
const noUpdateCheckEnv = "SPEEDTAIER_NO_UPDATE_CHECK"

// updateSource 使用与 install.sh 相同的 GITHUB_PROXY 加速前缀。
func updateSource() update.Source {
	return update.Source{Proxy: os.Getenv("GITHUB_PROXY")}
}

// updateCheckEnabled 决定是否检查新版本。只在交互式终端中检查：重定向 stderr 的脚本与定时任务
// 不检查，避免联网和多余输出；也可以用 --no-update-check 或环境变量关闭。
func updateCheckEnabled(noFlag bool, env string, stderrIsTerminal bool) bool {
	return !noFlag && env == "" && stderrIsTerminal
}

// startUpdateCheck 在后台检查新版本，返回的函数不阻塞：有新版本时给出版本号，否则返回空串。
func (a *app) startUpdateCheck(current string) func() string {
	if !updateCheckEnabled(a.cfg.noUpdateCheck, os.Getenv(noUpdateCheckEnv), isTerminal(os.Stderr)) {
		return func() string { return "" }
	}
	return update.Checker{Source: updateSource(), Cache: update.DefaultCache()}.Start(current)
}

// notice 提示有新版本。
func (u *ui) notice(latest, current string) {
	fmt.Fprint(os.Stderr, levelInfo.line(fmt.Sprintf("发现新版本 %s(当前 %s)，运行 speedtaier upgrade 升级", latest, current))+"\n")
}

func (a *app) newUpgradeCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "升级到最新版本",
		Long: `检查 GitHub Releases 上的最新版本，下载与当前平台匹配的发布包，校验 sha256 后替换当前程序。

访问 GitHub 困难时，可设置环境变量 GITHUB_PROXY 使用加速地址(与一键安装脚本相同)，
最终地址为 前缀 + 原始地址，如 GITHUB_PROXY=https://proxy.example.com/ speedtaier upgrade。
没有程序所在目录的写权限时请用 sudo 运行，或重新运行一键安装脚本。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.upgrade(cmd.Context(), readBuildInfo().Version, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "只检查是否有新版本，不安装")
	return cmd
}

func (a *app) upgrade(ctx context.Context, current string, checkOnly bool) error {
	ui := newUI(a.cfg.noProgress, false)
	src := updateSource()

	done := ui.Step("检查最新版本")
	latest, err := src.Latest(ctx)
	done()
	if err != nil {
		return fmt.Errorf("检查最新版本失败: %w；访问 GitHub 困难时可设置 GITHUB_PROXY 使用加速地址", err)
	}
	update.DefaultCache().Write(latest, time.Now())

	fmt.Print("\n" + levelInfo.line("当前版本 "+current) + levelInfo.line("最新版本 "+latest))
	_, release := update.ParseVersion(current)
	if release && !update.Newer(current, latest) {
		fmt.Print(levelSuccess.line("已是最新版本") + "\n")
		return nil
	}
	if checkOnly {
		fmt.Print(levelInfo.line("发现新版本，运行 speedtaier upgrade 升级") + "\n")
		return nil
	}
	if !release {
		fmt.Print(levelInfo.line(fmt.Sprintf("当前是开发版本 %s，将替换为正式版 %s", current, latest)))
	}

	exe, err := currentExecutable()
	if err != nil {
		return err
	}
	asset, err := update.AssetName(runtime.GOOS, runtime.GOARCH, buildSetting("GOARM"))
	if err != nil {
		return err
	}
	done = ui.Step("下载并校验 " + asset)
	err = src.Install(ctx, latest, asset, exe, runVersion)
	done()
	switch {
	case errors.Is(err, update.ErrNoWrite):
		return fmt.Errorf("没有写入权限(%s)；请用 sudo speedtaier upgrade，或重新运行一键安装脚本", filepath.Dir(exe))
	case err != nil:
		return fmt.Errorf("升级失败: %w", err)
	}
	fmt.Print(levelSuccess.line("已升级到 "+latest+"  "+exe) + "\n")
	return nil
}

// currentExecutable 返回当前程序的真实路径(解析符号链接)。go run 的临时程序不能升级。
func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法确定程序路径: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", fmt.Errorf("无法确定程序路径: %w", err)
	}
	if strings.Contains(filepath.ToSlash(exe), "/go-build") {
		return "", errors.New("当前是 go run 生成的临时程序，无法升级；请先安装 speedtaier")
	}
	return exe, nil
}

// runVersion 运行新程序的 version 命令，确认它能在本机执行(如架构匹配)。
func runVersion(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, path, "version").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// buildSetting 读取构建信息中的设置，如 GOARM；读不到时返回空串。
func buildSetting(key string) string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == key {
				return s.Value
			}
		}
	}
	return ""
}
