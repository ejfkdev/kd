package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const baseURL = "https://oapi.dingtalk.com"

// DumpError is a DingTalk business error (errcode != 0).
type DumpError struct {
	Code int
	Msg  string
}

func (e *DumpError) Error() string { return fmt.Sprintf("errcode %d: %s", e.Code, e.Msg) }

// invalidTokenCodes: the token failed gateway auth. Any other business error
// (403 scope denial, field validation, 404 ...) proves the token is VALID.
var invalidTokenCodes = map[int]bool{
	40014: true, // 不合法的 access_token
	40089: true, // 不合法的 corpId/appSecret (gettoken 场景)
	40096: true, // 不合法的 appKey/appSecret
	// errcode 88 是钉钉的通用包装码（subcode 才区分原因，含 60011 缺权限等），
	// 单独出现不能判为凭据无效。
}

// Dumper carries shared state for the whole run.
type Dumper struct {
	opt    *Options
	client *http.Client
	proxy  *url.URL

	accessToken string
	tokExp      time.Time
	tokMu       sync.Mutex

	limiter *rateLimiter
	out     *Output

	reqCnt   atomic.Int64
	errMu    sync.Mutex
	errCodes map[int]int

	resMu     sync.Mutex
	resources map[string]*resourceItem
	resOrder  []string

	memoMu sync.Mutex
	memo   map[string]interface{}
}

// NewDumper builds the HTTP plumbing; token acquisition happens lazily.
func NewDumper(opt *Options) (*Dumper, error) {
	tr := &http.Transport{MaxIdleConns: 32, MaxIdleConnsPerHost: 32, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second}
	d := &Dumper{
		opt: opt,
		client: &http.Client{
			Timeout:   time.Duration(opt.Timeout) * time.Second,
			Transport: tr,
		},
		limiter:   newRateLimiter(opt.QPS),
		errCodes:  map[int]int{},
		resources: map[string]*resourceItem{},
		memo:      map[string]interface{}{},
	}
	if opt.Proxy != "" {
		pu, err := url.Parse(opt.Proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid -proxy %q: %w", opt.Proxy, err)
		}
		d.proxy = pu
		tr.Proxy = http.ProxyURL(pu)
	}
	return d, nil
}

// token returns a valid access token, caching app-secret based ones.
func (d *Dumper) token(ctx context.Context, force bool) (string, error) {
	if d.opt.AppSecret == "" {
		if d.opt.AccessToken == "" {
			return "", fmt.Errorf("no access token available")
		}
		return d.opt.AccessToken, nil
	}
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	if !force && d.accessToken != "" && time.Now().Before(d.tokExp) {
		return d.accessToken, nil
	}
	u := baseURL + "/gettoken?appkey=" + url.QueryEscape(d.opt.AppKey) + "&appsecret=" + url.QueryEscape(d.opt.AppSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.ErrCode != 0 {
		return "", &DumpError{Code: r.ErrCode, Msg: r.ErrMsg}
	}
	d.accessToken = r.AccessToken
	if r.ExpiresIn <= 0 {
		r.ExpiresIn = 7200
	}
	d.tokExp = time.Now().Add(time.Duration(r.ExpiresIn-120) * time.Second)
	return d.accessToken, nil
}

func retryableErr(err error) bool {
	var de *DumpError
	if errors.As(err, &de) {
		return de.Code == 90018 || de.Code == 429 || de.Code == 40300
	}
	return strings.Contains(err.Error(), "timeout") || errors.Is(err, context.DeadlineExceeded)
}

// callSpec describes one raw request; most DingTalk APIs are JSON POSTs.
type callSpec struct {
	method string
	path   string // e.g. /topapi/v2/user/get
	query  map[string]string
	body   map[string]interface{}
	raw    bool // binary response
	noAuth bool // do not append access_token
}

func (c callSpec) url(ctx context.Context, d *Dumper) string {
	u := baseURL + c.path
	q := url.Values{}
	for k, v := range c.query {
		q.Set(k, v)
	}
	if !c.noAuth {
		tok, err := d.token(ctx, false)
		if err == nil {
			q.Set("access_token", tok)
		}
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// do executes one API call with retries + logging; returns the parsed body.
func (d *Dumper) do(ctx context.Context, spec callSpec) ([]byte, error) {
	for attempt := 0; attempt <= d.opt.Retry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
			}
		}
		d.limiter.Wait(ctx)
		d.reqCnt.Add(1)

		u := spec.url(ctx, d)
		var body io.Reader
		if spec.body != nil {
			buf, _ := json.Marshal(spec.body)
			body = bytes.NewReader(buf)
		}
		req, err := http.NewRequestWithContext(ctx, spec.method, u, body)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := d.client.Do(req)
		if err != nil {
			if retryableErr(err) {
				continue
			}
			return nil, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if spec.raw {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return data, nil
			}
			return nil, fmt.Errorf("http %d: %.200s", resp.StatusCode, string(data))
		}
		var r struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		d.logReq(u, r.ErrCode, r.ErrMsg, resp.StatusCode)
		if r.ErrCode != 0 {
			de := &DumpError{Code: r.ErrCode, Msg: r.ErrMsg}
			d.noteCode(r.ErrCode)
			if retryableErr(de) {
				continue
			}
			return data, de
		}
		return data, nil
	}
	return nil, fmt.Errorf("giving up after %d retries", d.opt.Retry)
}

func (d *Dumper) logReq(u string, code int, msg string, httpStatus int) {
	if code != 0 {
		d.reqLog("%s -> errcode %d: %s", u, code, msg)
		return
	}
	d.reqLog("[http %d] %s -> errcode 0", httpStatus, u)
}

func (d *Dumper) noteCode(code int) {
	d.errMu.Lock()
	d.errCodes[code]++
	d.errMu.Unlock()
}

// ErrorCodeSummary snapshot for meta.json.
func (d *Dumper) ErrorCodeSummary() map[string]int {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	out := map[string]int{}
	for k, v := range d.errCodes {
		out[strconv.Itoa(k)] = v
	}
	return out
}

func (d *Dumper) reqLog(format string, args ...interface{}) {
	if d.out == nil {
		return
	}
	d.out.Logf(format, args...)
}

func (d *Dumper) log(mod, msg string) { fmt.Printf("[%s] %s\n", mod, msg) }
func (d *Dumper) logf(mod, format string, args ...interface{}) {
	if d.opt.Verbose {
		fmt.Printf("[%s] %s\n", mod, fmt.Sprintf(format, args...))
	}
}

// memoized caches module results for dependent reuse.
func (d *Dumper) memoized(id string, fn func() (interface{}, error)) (interface{}, error) {
	d.memoMu.Lock()
	if v, ok := d.memo[id]; ok {
		d.memoMu.Unlock()
		return v, nil
	}
	d.memoMu.Unlock()
	v, err := fn()
	if err != nil {
		return nil, err
	}
	d.memoMu.Lock()
	d.memo[id] = v
	d.memoMu.Unlock()
	return v, nil
}

// ForEach parallelizes an indexed loop, preserving index determinism.
func (d *Dumper) ForEach(ctx context.Context, workers, n int, fn func(i int)) {
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	if workers == 0 {
		return
	}
	var idx int64 = -1
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				i := atomic.AddInt64(&idx, 1)
				if int(i) >= n {
					return
				}
				fn(int(i))
			}
		}()
	}
	wg.Wait()
}

// rateLimiter: simple token bucket refilled every second.
type rateLimiter struct {
	ticker *time.Ticker
	burst  chan struct{}
}

func newRateLimiter(qps int) *rateLimiter {
	burst := make(chan struct{}, qps)
	for i := 0; i < qps; i++ {
		burst <- struct{}{}
	}
	r := &rateLimiter{ticker: time.NewTicker(1 * time.Second), burst: burst}
	go func() {
		for range r.ticker.C {
			for i := 0; i < qps; i++ {
				select {
				case r.burst <- struct{}{}:
				default:
				}
			}
		}
	}()
	return r
}

func (r *rateLimiter) Wait(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-r.burst:
	}
}
