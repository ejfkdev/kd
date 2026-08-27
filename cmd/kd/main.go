// kd: 多产品 AK/SK 凭据数据备份工具，统一命令行入口。
//
//	kd feishu [flags]   飞书数据备份
//	kd dingtalk [flags] 钉钉数据备份
//	kd run [flags]      自动识别凭据归属产品，应用配置可省略
//	kd list / version
//
// 新产品接入：实现 internal/<name> 包，在 registry() 里注册一行 product.Spec 即可。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ejfkdev/kd/internal/dingtalk"
	"github.com/ejfkdev/kd/internal/feishu"
	"github.com/ejfkdev/kd/internal/product"
)

// version 由构建注入：go build -ldflags "-X main.version=v1.2.3"
var version = "dev"

const repoURL = "https://github.com/ejfkdev/kd"

// registry 是全部产品的注册表：新平台在此加一行。
func registry() []product.Spec {
	return []product.Spec{
		{
			Name: "feishu", Title: "飞书",
			DetectCredential: feishu.DetectCredential,
			TranslateFlags:   func(argv []string) []string { return argv },
			Run:              runFeishu,
			ListModules:      feishuModules,
		},
		{
			Name: "dingtalk", Title: "钉钉",
			DetectCredential: dingtalk.DetectCredential,
			TranslateFlags:   func(argv []string) []string { return translateFlag(argv, "-token", "-access-token") },
			Run:              runDingtalk,
			ListModules:      dingtalkModules,
		},
	}
}

func feishuModules() []product.ModuleInfo {
	out := []product.ModuleInfo{}
	for _, t := range feishu.ListTasks() {
		out = append(out, product.ModuleInfo{ID: t.ID, Group: t.Group, Desc: t.Desc})
	}
	return out
}

func dingtalkModules() []product.ModuleInfo {
	out := []product.ModuleInfo{}
	for _, t := range dingtalk.ListTasks() {
		out = append(out, product.ModuleInfo{ID: t.ID, Group: t.Group, Desc: t.Desc})
	}
	return out
}

func main() {
	args := os.Args[1:]
	reg := registry()
	if len(args) == 0 {
		usage(reg)
		os.Exit(2)
	}
	switch args[0] {
	case "feishu", "lark", "dingtalk":
		spec, ok := specByName(reg, args[0])
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
			usage(reg)
			os.Exit(2)
		}
		spec.Run(args[1:])
	case "run", "dump":
		runAuto(reg, args[1:])
	case "list":
		listProducts(reg)
	case "version", "-v", "--version":
		fmt.Printf("kd %s (%s)\n", version, repoURL)
	case "help", "-h", "--help":
		usage(reg)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
		usage(reg)
		os.Exit(2)
	}
}

func specByName(reg []product.Spec, name string) (product.Spec, bool) {
	if name == "lark" {
		name = "feishu"
	}
	for _, s := range reg {
		if s.Name == name {
			return s, true
		}
	}
	return product.Spec{}, false
}

// runAuto 遍历注册表，命中第一个能识别散凭据的产品后分发。
func runAuto(reg []product.Spec, argv []string) {
	appID := pickFlag(argv, "-app-id", "-id", "-app-key")
	token := pickFlag(argv, "-token", "-access-token")
	for _, spec := range reg {
		if spec.DetectCredential != nil && spec.DetectCredential(appID, token) {
			spec.Run(spec.TranslateFlags(argv))
			return
		}
	}
	fmt.Fprintf(os.Stderr, "error: 无法识别凭据归属产品：请提供 -app-id（cli_* 飞书 / ding* 钉钉）或 -token（t-/u- 飞书，其他为钉钉 access_token），或用 %s 显式指定\n", productNames(reg))
	os.Exit(2)
}

func productNames(reg []product.Spec) string {
	names := make([]string, 0, len(reg))
	for _, s := range reg {
		names = append(names, "kd "+s.Name)
	}
	return strings.Join(names, " / ")
}

func runFeishu(argv []string) {
	fs := flag.NewFlagSet("kd feishu", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "kd feishu — 飞书凭据数据备份\n\n用法:\n  kd feishu -app-id cli_xxx -app-secret yyy\n  kd feishu -token t-xxx | -user-token u-xxx | -tenant-token t-xxx | -app-token t-xxx\n  kd feishu -app-id cli_xxx -app-secret yyy -code <oauth-code> -code-redirect-uri <uri>\n\n参数:\n")
		fs.PrintDefaults()
	}
	opt, err := feishu.ParseFlags(fs, argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	run(func(ctx context.Context) error {
		d, err := feishu.NewDumper(opt)
		if err != nil {
			return err
		}
		return d.Run(ctx)
	})
}

func runDingtalk(argv []string) {
	fs := flag.NewFlagSet("kd dingtalk", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "kd dingtalk — 钉钉凭据数据备份\n\n用法:\n  kd dingtalk -app-key dingxxx -app-secret yyy   # 等价别名 -app-id\n  kd dingtalk -access-token <token>\n\n参数:\n")
		fs.PrintDefaults()
	}
	opt, err := dingtalk.ParseFlags(fs, argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	run(func(ctx context.Context) error {
		d, err := dingtalk.NewDumper(opt)
		if err != nil {
			return err
		}
		return d.Run(ctx)
	})
}

func pickFlag(argv []string, names ...string) string {
	for i := 0; i < len(argv)-1; i++ {
		for _, n := range names {
			if argv[i] == n {
				return argv[i+1]
			}
		}
	}
	return ""
}

func translateFlag(argv []string, from, to string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if argv[i] == from {
			out = append(out, to)
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

func run(fn func(ctx context.Context) error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := fn(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage(reg []product.Spec) {
	lines := make([]string, 0, len(reg))
	for _, s := range reg {
		lines = append(lines, fmt.Sprintf("  kd %-9s %s数据备份", s.Name, s.Title))
	}
	fmt.Fprintf(os.Stderr, `kd — 基于应用 Key 的数据备份工具 (%s)
仓库: %s

子命令:
%s
  kd run [flags]       自动识别凭据归属产品，应用配置可省略
  kd list              列出产品与数据模块清单
  kd version           版本信息

示例:
  kd run -app-id cli_xxx -app-secret yyy              # 自动识别为飞书
  kd run -app-id dingxxx -app-secret yyy             # 自动识别为钉钉
  kd run -token t-xxx                                # 飞书 token
  kd dingtalk -app-key dingxxx -app-secret yyy -x http://127.0.0.1:8080
  kd list

公共参数(各子命令内):
  -out <dir>  -qps N  -workers N  -retry N  -timeout N  -proxy/-x <url>
  -skip a,b  -only a,b  -max-items N  -verbose  -no-download
`, version, repoURL, strings.Join(lines, "\n"))
}

func listProducts(reg []product.Spec) {
	fmt.Println("kd 支持的产品:")
	fmt.Println()
	for _, spec := range reg {
		fmt.Printf("== %s (%s) ==\n", spec.Title, spec.Name)
		for _, t := range spec.ListModules() {
			fmt.Printf("  %-26s %s\n", t.ID, t.Desc)
		}
		fmt.Println()
	}
}
