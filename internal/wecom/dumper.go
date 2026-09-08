package wecom

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

const baseURL = "https://qyapi.weixin.qq.com/cgi-bin"

// DumpError is a WeCom business error (errcode != 0).
type DumpError struct {
	Code int
	Msg  string
}

func (e *DumpError) Error() string { return fmt.Sprintf("errcode %d: %s", e.Code, e.Msg) }

// invalidTokenCodes: 凭据本身无效。其余业务错误（60011 无权限、参数校验、
// 404 等）都证明 token 已通过网关鉴权。
var invalidTokenCodes = map[int]bool{
	40001: true, // secret 错误 / access_token 无效
	40013: true, // 不合法的 CorpID
	40014: true, // 不合法的 access_token
	40125: true, // 不合法的 corpsecret
	41001: true, // 缺少 access_token 参数
}

// retryableCodes: 限流/系统繁忙/token 过期（过期走强制刷新）。
var retryableCodes = map[int]bool{
	-1:    true, // 系统繁忙
	42001: true, // access_token 已过期
	45009: true, // 接口调用超过限制
	45011: true, // API 频率限制（分钟级）
	45033: true, // 接口并发调用超过限制
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

// token returns a valid access token, caching corp-secret based ones.
func (d *Dumper) token(ctx context.Context, force bool) (string, error) {
	if d.opt.CorpSecret == "" {
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
	u := baseURL + "/gettoken?corpid=" + url.QueryEscape(d.opt.CorpID) + "&corpsecret=" + url.QueryEscape(d.opt.CorpSecret)
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
		return retryableCodes[de.Code]
	}
	return strings.Contains(err.Error(), "timeout") || errors.Is(err, context.DeadlineExceeded)
}

// callSpec describes one raw request. WeCom mixes GET (通讯录/应用) and JSON
// POST (客户联系/审批/打卡) endpoints.
type callSpec struct {
	method string
	path   string // e.g. /user/get
	query  map[string]string
	body   map[string]interface{}
	raw    bool // binary response (media/get)
	noAuth bool // do not append access_token
}

func (c callSpec) url(ctx context.Context, d *Dumper, forceTok bool) string {
	u := baseURL + c.path
	q := url.Values{}
	for k, v := range c.query {
		q.Set(k, v)
	}
	if !c.noAuth {
		tok, err := d.token(ctx, forceTok)
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
// 42001/40014 with corp credentials triggers one forced token refresh.
func (d *Dumper) do(ctx context.Context, spec callSpec) ([]byte, error) {
	forcedRefresh := false
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

		u := spec.url(ctx, d, false)
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
			// media/get 失败时返回 JSON 错误体而非二进制
			if resp.StatusCode >= 200 && resp.StatusCode < 300 && !json.Valid(data) {
				return data, nil
			}
			if json.Valid(data) {
				var r struct {
					ErrCode int    `json:"errcode"`
					ErrMsg  string `json:"errmsg"`
				}
				if json.Unmarshal(data, &r) == nil && r.ErrCode != 0 {
					d.logReq(u, r.ErrCode, r.ErrMsg, resp.StatusCode)
					return nil, &DumpError{Code: r.ErrCode, Msg: r.ErrMsg}
				}
			}
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
			// token 过期/失效且持有 corp 凭据：强制刷新一次后重试
			if !forcedRefresh && (r.ErrCode == 42001 || r.ErrCode == 40014 || r.ErrCode == 40001) && d.opt.CorpSecret != "" {
				forcedRefresh = true
				d.reqLog("token refresh forced by errcode %d", r.ErrCode)
				if _, err := d.token(ctx, true); err != nil {
					return data, de
				}
				continue
			}
			if retryableErr(de) {
				continue
			}
			return data, de
		}
		return data, nil
	}
	return nil, fmt.Errorf("giving up after %d retries", d.opt.Retry)
}

// getJSON performs a GET and decodes into a generic map.
func (d *Dumper) getJSON(ctx context.Context, path string, query map[string]string) (map[string]interface{}, error) {
	data, err := d.do(ctx, callSpec{method: "GET", path: path, query: query})
	if err != nil {
		return nil, err
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", path, err)
	}
	return root, nil
}

// postJSON performs a JSON POST and decodes into a generic map.
func (d *Dumper) postJSON(ctx context.Context, path string, body map[string]interface{}) (map[string]interface{}, error) {
	data, err := d.do(ctx, callSpec{method: "POST", path: path, body: body})
	if err != nil {
		return nil, err
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", path, err)
	}
	return root, nil
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
