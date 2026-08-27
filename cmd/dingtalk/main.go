// dingtalk-dump: 钉钉 AK/AppSecret 数据尽取工具。
//
// 凭据形态：-app-key/-app-secret（自动换 access_token，缓存 2h）或 -access-token。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ejfkdev/kd/internal/dingtalk"
)

func main() {
	fs := flag.NewFlagSet("dingtalk-dump", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `dingtalk-dump - dump all DingTalk data reachable by a credential

Usage:
  dingtalk-dump -app-key dingxxx -app-secret yyy
  DINGTALK_APP_KEY=dingxxx DINGTALK_APP_SECRET=yyy dingtalk-dump
  dingtalk-dump -access-token <token>

Flags:
`)
		fs.PrintDefaults()
	}

	opt, err := dingtalk.ParseFlags(fs, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := dingtalk.NewDumper(opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := d.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
