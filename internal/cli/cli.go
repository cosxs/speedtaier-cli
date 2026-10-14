package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

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
	operator string

	threads      int
	seconds      int
	timeout      int
	latencyCount int

	noDownload    bool
	noUpload      bool
	noSingle      bool
	noProgress    bool
	noUpdateCheck bool
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
		Use: "speedtaier",
		// 不设 Short 与 Long：根命令的帮助只显示 Usage 与参数，说明见 README。
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
	pf.StringVar(&a.cfg.province, "province", "", "省份拼音，如 jiangsu、shandong；不指定时按 IP 归属地")
	pf.StringVar(&a.cfg.iface, "interface", runner.InterfaceAuto,
		"测速使用的网卡: auto(检测到透明代理时自动绑定物理网卡)、none(按系统路由)或网卡名如 en0")
	pf.BoolVar(&a.cfg.noProgress, "no-progress", false, "关闭实时进度")
	pf.BoolVar(&a.cfg.noUpdateCheck, "no-update-check", false, "不检查新版本(也可设置环境变量 "+noUpdateCheckEnv+"=1)")
	_ = cmd.RegisterFlagCompletionFunc("province",
		cobra.FixedCompletions(taier.KnownProvinces(), cobra.ShellCompDirectiveNoFileComp))

	f := cmd.Flags()
	f.Var(&a.cfg.family, "family", "测速协议: auto(本机有 IPv6 时加测 IPv6)、4、6")
	f.StringVar(&a.cfg.operator, "operator", runner.OperatorAuto,
		"测速的运营商: auto(本机运营商，按 IP 归属地)、all(全部运营商)或运营商名如 电信、liantong、mobile")
	f.IntVar(&a.cfg.threads, "threads", 8, "多线程测速的并发连接数；1 表示只测单线程")
	f.IntVar(&a.cfg.seconds, "seconds", 10, "每阶段最长计量秒数(不含预热)；吞吐稳定后提前结束")
	f.IntVar(&a.cfg.timeout, "timeout", 0, "单节点总超时秒，0 表示按测速时长自动计算")
	f.IntVar(&a.cfg.latencyCount, "latency-count", 20, "空载时延采样次数(抖动按这些样本计算)")
	f.BoolVar(&a.cfg.noSingle, "no-single", false, "不测单线程，只测多线程(--threads 条连接)")
	f.BoolVar(&a.cfg.noDownload, "no-download", false, "跳过下载测速")
	f.BoolVar(&a.cfg.noUpload, "no-upload", false, "跳过上传测速")
	_ = cmd.RegisterFlagCompletionFunc("family",
		cobra.FixedCompletions([]string{"auto", "4", "6"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("operator",
		cobra.FixedCompletions([]string{runner.OperatorAuto, runner.OperatorAll, "dianxin", "liantong", "yidong"},
			cobra.ShellCompDirectiveNoFileComp))

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "列出省份内全部节点及 IPv4/IPv6 连通性",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd.Context(), true)
		},
	})
	cmd.AddCommand(a.newUpgradeCmd())
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
		Operator:   a.cfg.operator,
		Test:       opts,
		SurveyOnly: surveyOnly,
	}

	current := readBuildInfo().Version
	newer := a.startUpdateCheck(current)
	ui := newUI(a.cfg.noProgress, surveyOnly)
	ui.banner(current)
	res := runner.Run(ctx, cfg, ui)
	ui.finish(res)
	if latest := newer(); latest != "" {
		ui.notice(latest, current)
	}
	return exitWith(res.ExitCode)
}

// testOptions 校验测速参数并转换为 taier.Options。
func (a *app) testOptions() (taier.Options, error) {
	c := a.cfg
	for _, p := range []struct {
		name  string
		value int
	}{{"threads", c.threads}, {"seconds", c.seconds}, {"latency-count", c.latencyCount}} {
		if p.value < 1 {
			return taier.Options{}, fmt.Errorf("--%s 必须大于 0", p.name)
		}
	}
	if c.timeout < 0 {
		return taier.Options{}, errors.New("--timeout 不能为负数")
	}

	opts := taier.Options{
		Threads:      c.threads,
		SingleThread: !c.noSingle,
		Seconds:      c.seconds,
		LatencyCount: c.latencyCount,
		Timeout:      time.Duration(c.timeout) * time.Second,
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
