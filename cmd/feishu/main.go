// feishu-dump: dump all data reachable by a Feishu (Lark) credential.
//
// Credentials can be provided as:
//   - app id + app secret (tenant_access_token auto-managed)
//   - a token directly: user_access_token (u-*), tenant_access_token (t-*),
//     app_access_token, or --token which auto-detects u-/t-
//   - an OAuth authorization code (--code + --code-redirect-uri) -> user token
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ejfkdev/kd/internal/feishu"
)

func main() {
	fs := flag.NewFlagSet("feishu-dump", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `feishu-dump - dump all Feishu data reachable by a credential

Usage:
  feishu-dump -app-id cli_xxx -app-secret yyy                     [tenant token mode]
  FEISHU_APP_ID=cli_xxx FEISHU_APP_SECRET=yyy feishu-dump
  feishu-dump -user-token u-...                                   [user token mode]
  feishu-dump -token t-...                                        [auto-detect]
  feishu-dump -app-id cli_xxx -app-secret yyy -code <oauth-code> -code-redirect-uri <uri>

Flags:
`)
		fs.PrintDefaults()
	}

	opt, err := feishu.ParseFlags(fs, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := feishu.NewDumper(opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := d.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
