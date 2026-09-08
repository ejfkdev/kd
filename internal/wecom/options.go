package wecom

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// Options holds CLI configuration for the wecom dump tool.
type Options struct {
	CorpID     string
	CorpSecret string

	// Direct access token (2h lifetime, refreshed automatically when corp creds present).
	AccessToken string

	Out     string
	QPS     int
	Retry   int
	Timeout int
	Proxy   string
	Workers int
	Skip    map[string]bool
	Only    map[string]bool

	NoDownload bool
	MaxItems   int
	Verbose    bool
}

func defaultOptions() *Options {
	return &Options{
		QPS:         20,
		Retry:       3,
		Timeout:     30,
		Workers:     8,
		Skip:        map[string]bool{},
		Only:        map[string]bool{},
		CorpID:      firstEnv("WECOM_CORP_ID", "WECOM_CORPID", "WXCORP_ID"),
		CorpSecret:  firstEnv("WECOM_CORP_SECRET", "WECOM_CORPSECRET", "WXCORP_SECRET"),
		AccessToken: os.Getenv("WECOM_ACCESS_TOKEN"),
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
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

// ParseFlags registers flags on fs and returns Options.
func ParseFlags(fs *flag.FlagSet, args []string) (*Options, error) {
	o := defaultOptions()

	fs.StringVar(&o.CorpID, "corpid", o.CorpID, "WeCom corp id, ww*/wx* (also WECOM_CORP_ID env)")
	fs.StringVar(&o.CorpID, "app-id", o.CorpID, "alias of -corpid")
	fs.StringVar(&o.CorpSecret, "corpsecret", o.CorpSecret, "WeCom corp secret (also WECOM_CORP_SECRET env)")
	fs.StringVar(&o.CorpSecret, "app-secret", o.CorpSecret, "alias of -corpsecret")
	fs.StringVar(&o.AccessToken, "access-token", o.AccessToken, "direct access_token (also WECOM_ACCESS_TOKEN env)")
	fs.StringVar(&o.Out, "out", "", "output directory (default: wecom_dump_<ts>/)")
	fs.IntVar(&o.QPS, "qps", o.QPS, "max requests per second")
	fs.IntVar(&o.Retry, "retry", o.Retry, "retry count for rate-limit/5xx errors")
	fs.IntVar(&o.Timeout, "timeout", o.Timeout, "per-request HTTP timeout in seconds (5-300)")
	fs.StringVar(&o.Proxy, "proxy", o.Proxy, "http/https proxy URL, e.g. http://127.0.0.1:8080")
	fs.StringVar(&o.Proxy, "x", o.Proxy, "alias of -proxy")
	fs.IntVar(&o.Workers, "workers", o.Workers, "concurrent workers for per-item loops (1-32)")
	noDownload := fs.Bool("no-download", o.NoDownload, "skip resource downloads")
	fs.IntVar(&o.MaxItems, "max-items", o.MaxItems, "max items per list, 0 = unlimited")
	fs.BoolVar(&o.Verbose, "verbose", o.Verbose, "verbose logging")

	var skip, only string
	fs.StringVar(&skip, "skip", "", "comma-separated modules to skip")
	fs.StringVar(&only, "only", "", "comma-separated modules to run only")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	o.NoDownload = *noDownload
	for _, m := range splitList(skip) {
		o.Skip[m] = true
	}
	for _, m := range splitList(only) {
		o.Only[m] = true
	}

	if o.QPS < 1 {
		o.QPS = 1
	}
	if o.QPS > 100 {
		o.QPS = 100
	}
	if o.Timeout < 5 {
		o.Timeout = 5
	}
	if o.Timeout > 300 {
		o.Timeout = 300
	}
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Workers > 32 {
		o.Workers = 32
	}
	if o.CorpSecret != "" && o.CorpID == "" || o.CorpID != "" && o.CorpSecret == "" {
		return nil, fmt.Errorf("-corpid and -corpsecret must be provided together")
	}
	if o.CorpSecret == "" && o.AccessToken == "" {
		return nil, fmt.Errorf("no credentials: provide -corpid/-corpsecret (or WECOM_CORP_ID/WECOM_CORP_SECRET) or -access-token")
	}
	return o, nil
}
