// wecom-dump: 企业微信 corpid/corpsecret 数据尽取工具。
//
// 凭据形态：-corpid/-corpsecret（自动换 access_token，缓存 2h）或 -access-token。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ejfkdev/kd/internal/wecom"
)

func main() {
	fs := flag.NewFlagSet("wecom-dump", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `wecom-dump - dump all WeCom (企业微信) data reachable by a credential

Usage:
  wecom-dump -corpid wwxxx -corpsecret yyy
  WECOM_CORP_ID=wwxxx WECOM_CORP_SECRET=yyy wecom-dump
  wecom-dump -access-token <token>

Flags:
`)
		fs.PrintDefaults()
	}

	opt, err := wecom.ParseFlags(fs, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := wecom.NewDumper(opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := d.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
