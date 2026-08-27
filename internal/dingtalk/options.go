package dingtalk

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// Options holds CLI configuration for the dingtalk dump tool.
type Options struct {
	AppKey    string
	AppSecret string

	// Direct access token (2h lifetime, refreshed automatically when app creds present).
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
		AppKey:      os.Getenv("DINGTALK_APP_KEY"),
		AppSecret:   os.Getenv("DINGTALK_APP_SECRET"),
		AccessToken: os.Getenv("DINGTALK_ACCESS_TOKEN"),
	}
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

	fs.StringVar(&o.AppKey, "app-key", o.AppKey, "DingTalk app key (also DINGTALK_APP_KEY env)")
	fs.StringVar(&o.AppKey, "app-id", o.AppKey, "alias of -app-key")
	fs.StringVar(&o.AppSecret, "app-secret", o.AppSecret, "DingTalk app secret (also DINGTALK_APP_SECRET env)")
	fs.StringVar(&o.AccessToken, "access-token", o.AccessToken, "direct access_token (also DINGTALK_ACCESS_TOKEN env)")
	fs.StringVar(&o.Out, "out", "", "output directory (default: dingtalk_dump_<ts>/)")
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
	if o.AppSecret != "" && o.AppKey == "" || o.AppKey != "" && o.AppSecret == "" {
		return nil, fmt.Errorf("-app-key and -app-secret must be provided together")
	}
	if o.AppSecret == "" && o.AccessToken == "" {
		return nil, fmt.Errorf("no credentials: provide -app-key/-app-secret (or DINGTALK_APP_KEY/DINGTALK_APP_SECRET) or -access-token")
	}
	return o, nil
}
