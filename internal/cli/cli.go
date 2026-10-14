package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

const longDescription = `全球网测命令行测速工具。

节点来源: 官方全量节点库(IPv4，按清单 MD5 本地缓存)，IPv6 地址通过官方匹配接口补全。
选定省份后列出该省全部节点及 IPv4/IPv6 连通性，再为每个运营商选择时延最低的节点测速，
失败时自动换下一个节点。不指定 --province 时交互选择(默认 IP 归属地)，非交互环境直接使用归属地。
检测到透明代理(Surge、Clash 等 TUN 模式)时，测速流量自动改由物理网卡发出。

示例:
  speedtaier                                 # 选择省份并测速
  speedtaier --province jiangsu              # 指定省份(拼音)
  speedtaier --province jiangsu --family 4   # 只测 IPv4
  speedtaier list --province guangdong       # 仅查看节点及连通性
  speedtaier --province jiangsu --json       # 输出 JSON(适合脚本与定时任务)
  speedtaier completion zsh > _speedtaier    # shell 补全(可补全省份拼音)

省份拼音: 陕西为 shaanxi，山西为 shanxi，内蒙古为 neimenggu，西藏为 xizang。

退出码: 0 全部成功 · 1 启动失败 · 2 部分测试失败或存在警告 · 130 已中断`

// exitCode 作为 error 返回时表示相关信息已输出，Main 直接以该值退出。
type exitCode int

func (c exitCode) Error() string { return "exit status " + strconv.Itoa(int(c)) }

func exitWith(code int) error {
	if code == runner.ExitOK {
		return nil
	}
	return exitCode(code)
}

// familyFlag 让 --family 在解析参数时即校验取值。
type familyFlag struct{ mode runner.FamilyMode }

func (f *familyFlag) String() string { return string(f.mode) }
func (f *familyFlag) Type() string   { return "family" }

func (f *familyFlag) Set(s string) error {
	m, err := runner.ParseFamilyMode(s)
	if err != nil {
		return err
	}
	f.mode = m
	return nil
}

type config struct {
	province string
	family   familyFlag
	iface    string

	threads      int
	seconds      int
	bandwidth    int
	timeout      int
	latencyCount int

	imei           string
	noDownload     bool
	noUpload       bool
	noProgress     bool
	json           bool
	nonInteractive bool
}

type app struct {
	cfg config
}

func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// 第一次中断后恢复默认信号处理，再按一次 Ctrl-C 可立即退出。
	context.AfterFunc(ctx, stop)

	a := &app{cfg: config{family: familyFlag{runner.FamilyAuto}}}
	root := a.newRootCmd()
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		if code, ok := errors.AsType[exitCode](err); ok {
			return int(code)
		}
		fmt.Fprintf(os.Stderr, "错误: %v\n运行 speedtaier --help 查看用法\n", err)
		return runner.ExitFailed
	}
	return runner.ExitOK
}

func (a *app) newRootCmd() *cobra.Command {
	build := readBuildInfo()
	cmd := &cobra.Command{
		Use:           "speedtaier",
		Short:         "全球网测命令行测速工具",
		Long:          longDescription,
		Version:       build.String(),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd.Context(), false)
		},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")

	pf := cmd.PersistentFlags()
	pf.StringVar(&a.cfg.province, "province", "", "省份拼音，如 jiangsu、shandong；不指定时交互选择(默认 IP 归属地)")
	pf.StringVar(&a.cfg.iface, "interface", runner.InterfaceAuto,
		"测速使用的网卡: auto(检测到透明代理时自动绑定物理网卡)、none(按系统路由)或网卡名如 en0")
	pf.BoolVar(&a.cfg.json, "json", false, "向 stdout 输出单个 JSON 文档(不交互、不显示进度)")
	pf.BoolVar(&a.cfg.nonInteractive, "non-interactive", false, "不交互选择省份，未指定时直接使用归属地")
	pf.BoolVar(&a.cfg.noProgress, "no-progress", false, "关闭实时进度")
	_ = cmd.RegisterFlagCompletionFunc("province",
		cobra.FixedCompletions(taier.KnownProvinces(), cobra.ShellCompDirectiveNoFileComp))

	f := cmd.Flags()
	f.Var(&a.cfg.family, "family", "测速协议: auto(本机有 IPv6 时加测 IPv6)、4、6")
	f.IntVar(&a.cfg.threads, "threads", 8, "并发连接数")
	f.IntVar(&a.cfg.seconds, "seconds", 10, "每阶段最长计量秒数(不含预热)；吞吐稳定后提前结束")
	f.IntVar(&a.cfg.bandwidth, "bandwidth", 200, "带宽参数 Mbps")
	f.IntVar(&a.cfg.timeout, "timeout", 0, "单节点总超时秒，0 表示按测速时长自动计算")
	f.IntVar(&a.cfg.latencyCount, "latency-count", 8, "空载时延采样次数")
	f.BoolVar(&a.cfg.noDownload, "no-download", false, "跳过下载测速")
	f.BoolVar(&a.cfg.noUpload, "no-upload", false, "跳过上传测速")
	f.StringVar(&a.cfg.imei, "imei", "", "固定 IMEI(调试用)")
	_ = cmd.RegisterFlagCompletionFunc("family",
		cobra.FixedCompletions([]string{"auto", "4", "6"}, cobra.ShellCompDirectiveNoFileComp))

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "列出省份内全部节点及 IPv4/IPv6 连通性",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd.Context(), true)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "显示版本",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Println(build)
		},
	})
	return cmd
}

func (a *app) run(ctx context.Context, surveyOnly bool) error {
	opts, err := a.testOptions()
	if err != nil {
		return err
	}
	cfg := runner.Config{
		Province:   a.cfg.province,
		Family:     a.cfg.family.mode,
		Interface:  a.cfg.iface,
		Test:       opts,
		SurveyOnly: surveyOnly,
		Version:    readBuildInfo().Version,
	}

	if a.cfg.json {
		res := runner.Run(ctx, cfg, runner.Discard)
		if err := writeJSON(os.Stdout, res); err != nil {
			fmt.Fprintf(os.Stderr, "输出 JSON 失败: %v\n", err)
			return exitCode(runner.ExitFailed)
		}
		return exitWith(res.ExitCode)
	}

	ui := newUI(a.cfg.noProgress, surveyOnly)
	if !a.cfg.nonInteractive && isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		cfg.ChooseProvince = chooseProvince
	}
	res := runner.Run(ctx, cfg, ui)
	ui.finish(res)
	return exitWith(res.ExitCode)
}

// writeJSON 输出单行 JSON，便于按行追加到日志文件(JSON Lines)。
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// testOptions 校验测速参数并转换为 taier.Options。
func (a *app) testOptions() (taier.Options, error) {
	c := a.cfg
	for _, p := range []struct {
		name  string
		value int
	}{{"threads", c.threads}, {"seconds", c.seconds}, {"bandwidth", c.bandwidth}, {"latency-count", c.latencyCount}} {
		if p.value < 1 {
			return taier.Options{}, fmt.Errorf("--%s 必须大于 0", p.name)
		}
	}
	if c.timeout < 0 {
		return taier.Options{}, errors.New("--timeout 不能为负数")
	}

	opts := taier.Options{
		Threads:      c.threads,
		Seconds:      c.seconds,
		Bandwidth:    c.bandwidth,
		LatencyCount: c.latencyCount,
		Timeout:      time.Duration(c.timeout) * time.Second,
		IMEI:         c.imei,
		SkipDownload: c.noDownload,
		SkipUpload:   c.noUpload,
	}
	// 显式指定的超时须容纳预热与各测速阶段的最长时长。
	if need := opts.Budget(); opts.Timeout > 0 && opts.Timeout < need {
		return taier.Options{}, fmt.Errorf("--timeout %d 秒不足以完成测速(预热与最长计量时长)，至少需要 %d 秒",
			c.timeout, int(need.Round(time.Second).Seconds()))
	}
	return opts, nil
}

// chooseProvince 的选项同时显示中文和拼音(如 "山东 shandong")，可直接输入拼音过滤。
func chooseProvince(provinces []string, def string) (string, error) {
	labels := make([]string, len(provinces))
	byLabel := make(map[string]string, len(provinces))
	for i, name := range provinces {
		labels[i] = strings.TrimSpace(name + " " + taier.ProvincePinyin(name))
		byLabel[labels[i]] = name
	}
	defLabel := ""
	if i := slices.Index(provinces, def); i >= 0 {
		defLabel = labels[i]
	}

	interrupted := false
	label, err := pterm.DefaultInteractiveSelect.
		WithOptions(labels).
		WithDefaultOption(defLabel).
		WithMaxHeight(10).
		WithOnInterruptFunc(func() { interrupted = true }).
		Show("选择省份(可输入拼音过滤)")
	switch {
	case interrupted:
		return "", runner.ErrInterrupted
	case err != nil:
		return "", fmt.Errorf("交互选择失败: %w；可用 --province 指定省份", err)
	}
	return byLabel[label], nil
}
