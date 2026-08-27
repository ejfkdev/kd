package feishu

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// Options holds all CLI configuration for the dump tool.
type Options struct {
	Host string // feishu | lark

	// Credentials: app id + secret (internal app) -> tenant/app token auto-managed.
	AppID     string
	AppSecret string

	// Direct tokens.
	UserToken   string
	TenantToken string
	AppToken    string
	Token       string // auto-detect by prefix (u- => user, t- => tenant)

	// Authorization code exchange (OAuth): produces a user token.
	Code            string
	CodeRedirectURI string

	Out     string // output json path
	QPS     int
	Retry   int
	Timeout int    // per-request HTTP timeout in seconds
	Proxy   string // http/https proxy URL

	Skip map[string]bool
	Only map[string]bool

	NoDownload  bool
	NoReactions bool
	NoMessages  bool
	UserDetails bool

	CalFrom string
	CalTo   string

	// DownloadThreads is the number of concurrent resource download workers.
	DownloadThreads int

	// Workers is the number of concurrent workers for per-item module loops
	// (per-chat history, per-message reactions, per-user details, ...).
	Workers int

	// Resume skips tasks whose output files already exist.
	Resume bool

	// RefreshToken captured during code exchange; reported in meta.
	RefreshToken string

	UserIDType string

	MaxItems int // per-list item cap, 0 = unlimited

	Verbose bool
}

func defaultOptions() *Options {
	o := &Options{
		Host:            "feishu",
		Out:             "",
		QPS:             20,
		Retry:           3,
		Timeout:         30,
		NoDownload:      false,
		NoReactions:     false,
		NoMessages:      false,
		UserDetails:     true,
		UserIDType:      "open_id",
		MaxItems:        0,
		CalFrom:         time.Now().AddDate(-1, 0, 0).Format("2006-01-02 15:04:05"),
		CalTo:           time.Now().AddDate(1, 0, 0).Format("2006-01-02 15:04:05"),
		DownloadThreads: 4,
		Workers:         8,
		Skip:            map[string]bool{},
		Only:            map[string]bool{},
		AppID:           os.Getenv("FEISHU_APP_ID"),
		AppSecret:       os.Getenv("FEISHU_APP_SECRET"),
		UserToken:       os.Getenv("FEISHU_USER_TOKEN"),
		TenantToken:     os.Getenv("FEISHU_TENANT_TOKEN"),
	}
	return o
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseFlags registers flags on fs and returns the resulting Options.
func ParseFlags(fs *flag.FlagSet, args []string) (*Options, error) {
	o := defaultOptions()

	fs.StringVar(&o.Host, "host", o.Host, "open platform host: feishu or lark")
	fs.StringVar(&o.AppID, "app-id", o.AppID, "app id (cli_xxx), also FEISHU_APP_ID env")
	fs.StringVar(&o.AppSecret, "app-secret", o.AppSecret, "app secret, also FEISHU_APP_SECRET env")
	fs.StringVar(&o.UserToken, "user-token", o.UserToken, "user_access_token (u-xxx), also FEISHU_USER_TOKEN env")
	fs.StringVar(&o.TenantToken, "tenant-token", o.TenantToken, "tenant_access_token (t-xxx), also FEISHU_TENANT_TOKEN env")
	fs.StringVar(&o.AppToken, "app-token", o.AppToken, "app_access_token (t-xxx)")
	fs.StringVar(&o.Token, "token", "", "any token, auto-detect: u-* -> user, t-* -> tenant")
	fs.StringVar(&o.Code, "code", "", "OAuth authorization code, exchange for user token")
	fs.StringVar(&o.CodeRedirectURI, "code-redirect-uri", "", "redirect_uri used when requesting the code")
	fs.StringVar(&o.Out, "out", "", "output directory (default: feishu_dump_<ts>/); all jsons + resources go under it")
	fs.IntVar(&o.QPS, "qps", o.QPS, "max requests per second (default 20; per-endpoint budgets auto-sleep as backstop)")
	fs.IntVar(&o.Retry, "retry", o.Retry, "retry count for rate-limit/5xx errors")
	fs.IntVar(&o.Timeout, "timeout", o.Timeout, "per-request HTTP timeout in seconds (5-300)")
	fs.StringVar(&o.Proxy, "proxy", o.Proxy, "http/https proxy URL, e.g. http://127.0.0.1:8080")
	fs.StringVar(&o.Proxy, "x", o.Proxy, "alias of -proxy")
	noDownload := fs.Bool("no-download", o.NoDownload, "skip binary file downloads (drive files, message resources)")
	noReactions := fs.Bool("no-reactions", o.NoReactions, "skip message reactions")
	noMessages := fs.Bool("no-messages", o.NoMessages, "skip chat message history")
	noUserDetails := fs.Bool("no-user-details", !o.UserDetails, "skip per-user detail fetch")
	fs.IntVar(&o.DownloadThreads, "download-threads", o.DownloadThreads, "concurrent resource download threads (1-32)")
	fs.IntVar(&o.Workers, "workers", o.Workers, "concurrent workers for per-item loops (1-32)")
	resume := fs.Bool("resume", o.Resume, "skip tasks whose output already exists (incremental rerun)")
	fs.StringVar(&o.CalFrom, "cal-from", o.CalFrom, "calendar events window start (default 1 year ago)")
	fs.StringVar(&o.CalTo, "cal-to", o.CalTo, "calendar events window end (default 1 year ahead)")
	fs.StringVar(&o.UserIDType, "user-id-type", o.UserIDType, "user id type: open_id / user_id / union_id")
	fs.IntVar(&o.MaxItems, "max-items", o.MaxItems, "max items per list, 0 = unlimited")
	fs.BoolVar(&o.Verbose, "verbose", o.Verbose, "verbose logging (per-page progress)")

	var skip, only string
	fs.StringVar(&skip, "skip", "", "comma-separated modules to skip")
	fs.StringVar(&only, "only", "", "comma-separated modules to run only")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	o.NoDownload = *noDownload
	o.Resume = *resume
	o.NoReactions = *noReactions
	o.NoMessages = *noMessages
	o.UserDetails = !*noUserDetails

	for _, m := range splitList(skip) {
		o.Skip[m] = true
	}
	for _, m := range splitList(only) {
		o.Only[m] = true
	}

	if o.Token != "" {
		switch {
		case strings.HasPrefix(o.Token, "u-"):
			if o.UserToken == "" {
				o.UserToken = o.Token
			}
		default: // t-* and anything else -> tenant by default
			if o.TenantToken == "" {
				o.TenantToken = o.Token
			}
		}
	}

	if o.QPS < 1 {
		o.QPS = 1
	}
	if o.QPS > 100 {
		o.QPS = 100
	}
	if o.DownloadThreads < 1 {
		o.DownloadThreads = 1
	}
	if o.DownloadThreads > 32 {
		o.DownloadThreads = 32
	}
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Workers > 32 {
		o.Workers = 32
	}
	if o.Retry < 0 {
		o.Retry = 0
	}
	if o.Timeout < 5 {
		o.Timeout = 5
	}
	if o.Timeout > 300 {
		o.Timeout = 300
	}
	if o.Host != "feishu" && o.Host != "lark" {
		return nil, fmt.Errorf("invalid -host %q (want feishu or lark)", o.Host)
	}
	if o.UserIDType != "open_id" && o.UserIDType != "user_id" && o.UserIDType != "union_id" {
		return nil, fmt.Errorf("invalid -user-id-type %q (want open_id, user_id or union_id)", o.UserIDType)
	}

	hasCreds := o.AppID != "" && o.AppSecret != ""
	if o.Code != "" && !hasCreds {
		return nil, fmt.Errorf("-code exchange requires -app-id and -app-secret")
	}
	if !hasCreds && o.UserToken == "" && o.TenantToken == "" && o.AppToken == "" {
		return nil, fmt.Errorf("no credentials: provide -app-id/-app-secret (or FEISHU_APP_ID/FEISHU_APP_SECRET), -user-token, -tenant-token, -app-token or -token")
	}

	return o, nil
}
