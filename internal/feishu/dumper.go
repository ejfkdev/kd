package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/core/accesstoken/authorizationcode"
)

// Supports describes which access token kinds an API accepts.
type Supports uint8

const (
	SupUser Supports = 1 << iota
	SupTenant
	SupApp
)

// TokenMode is how the tool authenticates.
type TokenMode string

const (
	ModeInternal   TokenMode = "app_id+app_secret"
	ModeUserDirect TokenMode = "user_access_token"
	ModeTenantDir  TokenMode = "tenant_access_token"
	ModeAppDirect  TokenMode = "app_access_token"
)

// FeishuError is a business error returned by the OpenAPI (code != 0).
type FeishuError struct {
	Code int
	Msg  string
	HTTP int
}

func (e *FeishuError) Error() string {
	if e.HTTP != 0 {
		return fmt.Sprintf("http %d, feishu code %d: %s", e.HTTP, e.Code, e.Msg)
	}
	return fmt.Sprintf("feishu code %d: %s", e.Code, e.Msg)
}

// Dumper carries shared state across all data modules.
type Dumper struct {
	opt     *Options
	client  *lark.Client
	baseURL string

	userToken   string // explicit user token (direct, --token or code exchange)
	tenantToken string // explicit tenant token
	appToken    string // explicit app token
	internal    bool   // app id+secret provided -> SDK auto-manages tenant/app tokens

	limiter *rateLimiter

	out      *Output
	driveIdx *driveIndex
	reqCnt   atomic.Int64
	mu       sync.Mutex

	// per-endpoint rate-limit budgets: Feishu limits each API independently and
	// reports remaining/reset counters in the response headers.
	budgeMu sync.Mutex
	budgets map[string]*endpointBudget

	// discovered resources (attachments/images) queued for the download stage
	resMu     sync.Mutex
	resources map[string]*resourceItem
	resOrder  []string

	// download plumbing: streaming/resumable fetches use a plain http client
	// (the SDK buffers whole bodies) plus a self-managed tenant token cache.
	httpClient *http.Client
	tokMu      sync.Mutex
	tokVal     string
	tokExp     time.Time

	// module result cache: dependent modules reuse fetched lists instead of
	// refetching (notably the message history used by reactions/resources).
	memoMu sync.Mutex
	memo   map[string]interface{}

	// id pool: resource ids harvested from readable data (messages etc) for
	// query-by-id modules when list APIs are not accessible.
	poolMu sync.Mutex
	idPool map[string]map[string]string

	// business error tally for the meta summary
	errMu    sync.Mutex
	errCodes map[int]int
}

// endpointBudget is the last-seen rate budget of one normalized API path.
type endpointBudget struct {
	remaining int
	has       bool
	reset     time.Time
}

// CallSpec describes one raw OpenAPI call.
type CallSpec struct {
	Method   string
	Path     string
	Paths    map[string]string
	Query    larkcore.QueryParams
	Body     interface{}
	Supports Supports
	Download bool // treat response as binary
	Quiet    bool // do not emit progress log
}

// NewDumper builds the SDK client and resolves tokens from options.
func NewDumper(opt *Options) (*Dumper, error) {
	d := &Dumper{
		opt:         opt,
		userToken:   opt.UserToken,
		tenantToken: opt.TenantToken,
		appToken:    opt.AppToken,
		internal:    opt.AppID != "" && opt.AppSecret != "",
		limiter:     newRateLimiter(opt.QPS),
		budgets:     map[string]*endpointBudget{},
		resources:   map[string]*resourceItem{},
		memo:        map[string]interface{}{},
	}

	baseURL := lark.FeishuBaseUrl
	if opt.Host == "lark" {
		baseURL = lark.LarkBaseUrl
	}
	d.baseURL = baseURL
	tr := &http.Transport{
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: time.Duration(opt.Timeout) * time.Second,
	}
	d.httpClient = &http.Client{Transport: tr}

	var dopts []lark.ClientOptionFunc
	dopts = append(dopts, lark.WithOpenBaseUrl(baseURL))
	if opt.Host == "lark" {
		// Lark 国际站与飞书的 accounts 域名不同，显式钉死，OAuth 换 token 不走错域
		dopts = append(dopts, lark.WithOAuthBaseUrl(lark.OAuthBaseUrlLark))
	} else {
		dopts = append(dopts, lark.WithOAuthBaseUrl(lark.OAuthBaseUrlFeishu))
	}
	dopts = append(dopts, lark.WithReqTimeout(time.Duration(opt.Timeout)*time.Second))
	dopts = append(dopts, lark.WithLogLevel(larkcore.LogLevelError))
	if opt.Proxy != "" {
		pu, err := url.Parse(opt.Proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid -proxy %q: %w", opt.Proxy, err)
		}
		tr.Proxy = http.ProxyURL(pu)
		dopts = append(dopts, lark.WithHttpClient(&http.Client{Transport: tr}))
	}

	appID := opt.AppID
	secret := opt.AppSecret
	if appID == "" {
		// Direct-token mode: the SDK validates that AppId is non-empty, supply a dummy.
		appID = "cli_dummy"
	}
	d.client = lark.NewClient(appID, secret, dopts...)

	// Exchange authorization code for a user token.
	if opt.Code != "" {
		req := authorizationcode.NewTokenRequestBuilder().
			Code(opt.Code).
			RedirectUri(opt.CodeRedirectURI).
			Build()
		resp, err := d.client.AccessToken.RetrieveByAuthorizationCode(context.Background(), req)
		if err != nil {
			return nil, fmt.Errorf("exchange code: %w", err)
		}
		if resp.StatusCode != http.StatusOK || resp.Data == nil || resp.Data.AccessToken == nil || *resp.Data.AccessToken == "" {
			return nil, fmt.Errorf("exchange code: http %d, %s", resp.StatusCode, string(resp.RawBody))
		}
		d.userToken = *resp.Data.AccessToken
		if resp.Data.RefreshToken != nil && *resp.Data.RefreshToken != "" {
			d.opt.RefreshToken = *resp.Data.RefreshToken
		}
	}

	return d, nil
}

// pick decides which token to use for the given supported types, and builds
// the SDK request options. Priority: user > tenant > app.
func (d *Dumper) pick(s Supports) ([]larkcore.AccessTokenType, []larkcore.RequestOptionFunc, error) {
	if s&SupUser != 0 && d.userToken != "" {
		return []larkcore.AccessTokenType{larkcore.AccessTokenTypeUser},
			[]larkcore.RequestOptionFunc{larkcore.WithUserAccessToken(d.userToken)}, nil
	}
	if s&SupTenant != 0 {
		if d.tenantToken != "" {
			return []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
				[]larkcore.RequestOptionFunc{larkcore.WithTenantAccessToken(d.tenantToken)}, nil
		}
		if d.internal {
			// Let the SDK fetch/cache/refresh the tenant token via app secret.
			return []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant}, nil, nil
		}
	}
	if s&SupApp != 0 {
		if d.appToken != "" {
			return []larkcore.AccessTokenType{larkcore.AccessTokenTypeApp},
				[]larkcore.RequestOptionFunc{func(o *larkcore.RequestOption) { o.AppAccessToken = d.appToken }}, nil
		}
		if d.internal {
			// Fetch an app token once and cache it locally.
			tok, err := d.fetchAppToken()
			if err != nil {
				return nil, nil, err
			}
			return []larkcore.AccessTokenType{larkcore.AccessTokenTypeApp},
				[]larkcore.RequestOptionFunc{func(o *larkcore.RequestOption) { o.AppAccessToken = tok }}, nil
		}
	}
	if s&SupUser != 0 {
		return nil, nil, fmt.Errorf("api requires user_access_token but no user token is available")
	}
	if s&SupTenant != 0 {
		return nil, nil, fmt.Errorf("api requires tenant_access_token but no tenant token is available")
	}
	return nil, nil, fmt.Errorf("no usable access token for this api")
}

var appTokenOnce sync.Once
var appTokenVal string
var appTokenErr error

func (d *Dumper) fetchAppToken() (string, error) {
	appTokenOnce.Do(func() {
		req := &larkcore.SelfBuiltAppAccessTokenReq{AppID: d.opt.AppID, AppSecret: d.opt.AppSecret}
		resp, err := d.client.GetAppAccessTokenBySelfBuiltApp(context.Background(), req)
		if err != nil {
			appTokenErr = err
			return
		}
		if resp.Code != 0 {
			appTokenErr = fmt.Errorf("app token: %s", resp.Msg)
			return
		}
		appTokenVal = resp.AppAccessToken
	})
	return appTokenVal, appTokenErr
}

func retryableErr(err error) bool {
	var fe *FeishuError
	if errors.As(err, &fe) {
		return fe.HTTP == 429 || fe.Code == 99991400
	}
	return osTimeout(err) || strings.Contains(err.Error(), "timeout")
}

func osTimeout(err error) bool {
	return err == context.DeadlineExceeded
}

// do performs a raw API call with retry + local rate limiting, returning the SDK response.
func (d *Dumper) do(ctx context.Context, spec CallSpec) (*larkcore.ApiResp, error) {
	supported, opts, err := d.pick(spec.Supports)
	if err != nil {
		return nil, err
	}
	if supported == nil {
		return nil, errors.New("no supported access token types")
	}
	apiReq := &larkcore.ApiReq{
		HttpMethod:                spec.Method,
		ApiPath:                   spec.Path,
		QueryParams:               spec.Query,
		PathParams:                larkcore.PathParams(spec.Paths),
		Body:                      spec.Body,
		SupportedAccessTokenTypes: supported,
	}

	var lastErr error
	norm := normalizePath(spec.Path)
	for attempt := 0; attempt <= d.opt.Retry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
			}
		}
		// per-endpoint budget: if the last response for this API said we are
		// nearly out of quota, sleep until the reset time before retrying it
		d.waitBudget(ctx, norm)
		d.limiter.Wait(ctx)
		d.reqCnt.Add(1)
		resp, err := d.client.Do(ctx, apiReq, opts...)
		if err != nil {
			lastErr = err
			if retryableErr(err) {
				continue
			}
			return nil, err
		}
		d.noteBudget(norm, resp.Header)
		if resp.StatusCode == 429 {
			lastErr = fmt.Errorf("http 429 rate limited")
			d.reqLog("%s %s -> http 429 (rate limited)", spec.Method, spec.Path)
			d.sleepRetryAfter(ctx, resp.Header)
			continue
		}
		if spec.Download {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return resp, nil
			}
			lastErr = &FeishuError{Code: resp.StatusCode, Msg: trimBody(resp.RawBody), HTTP: resp.StatusCode}
			if resp.StatusCode >= 500 {
				continue
			}
			return nil, lastErr
		}
		fe := parseFeishuError(resp.RawBody, resp.StatusCode)
		if fe != nil {
			lastErr = fe
			if fe.HTTP == 429 || fe.Code == 99991400 {
				d.reqLog("%s %s -> %s (rate limited)", spec.Method, spec.Path, fe.Error())
				continue
			}
			d.noteErrorCode(fe.Code)
			d.reqLog("%s %s -> %s", spec.Method, spec.Path, fe.Error())
			return resp, fe
		}
		d.reqLog("%s %s -> http %d, feishu code 0", spec.Method, spec.Path, resp.StatusCode)
		return resp, nil
	}
	return nil, fmt.Errorf("giving up after %d retries: %w", d.opt.Retry, lastErr)
}

func trimBody(raw []byte) string {
	s := string(raw)
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

func parseFeishuError(raw []byte, status int) *FeishuError {
	var probe struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		if status >= 200 && status < 300 {
			return nil
		}
		return &FeishuError{Code: status, Msg: trimBody(raw), HTTP: status}
	}
	if probe.Code != 0 {
		return &FeishuError{Code: probe.Code, Msg: probe.Msg, HTTP: status}
	}
	return nil
}

// reqLog writes a request/response summary line to run.log.
func (d *Dumper) reqLog(format string, args ...interface{}) {
	if d.out == nil {
		return
	}
	d.out.Logf(format, args...)
}

// memoized caches a module's result so dependent modules reuse it.
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

// logf prints progress with a module prefix.
func (d *Dumper) logf(mod string, format string, args ...interface{}) {
	if d.opt.Verbose {
		fmt.Printf("[%s] %s\n", mod, fmt.Sprintf(format, args...))
	}
}

func (d *Dumper) log(mod string, msg string) {
	fmt.Printf("[%s] %s\n", mod, msg)
}

// rateLimiter is a simple token bucket sized 1, refilled every 1/qps seconds.
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
		return
	case <-r.burst:
	}
}

// downloadToken returns the token used for streaming downloads. Priority:
// user > tenant > app; internal mode fetches/caches a tenant token itself
// (the SDK cache is not exposed, and download streams bypass the SDK client).
func (d *Dumper) downloadToken(ctx context.Context, force bool) (string, error) {
	if d.userToken != "" {
		return d.userToken, nil
	}
	if d.tenantToken != "" {
		return d.tenantToken, nil
	}
	if d.appToken != "" {
		return d.appToken, nil
	}
	if !d.internal {
		return "", fmt.Errorf("no token available for downloads")
	}
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	if !force && d.tokVal != "" && time.Now().Before(d.tokExp) {
		return d.tokVal, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": d.opt.AppID, "app_secret": d.opt.AppSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.Code != 0 {
		return "", fmt.Errorf("tenant token: code %d, %s", r.Code, r.Msg)
	}
	d.tokVal = r.TenantAccessToken
	if r.Expire <= 0 {
		r.Expire = 7200
	}
	d.tokExp = time.Now().Add(time.Duration(r.Expire-120) * time.Second)
	return d.tokVal, nil
}

// invalidTokenCodes are the business codes meaning "this token failed gateway
// authentication". Everything else (scope denials, field validations, 404s)
// proves the token already passed auth and therefore is a VALID credential.
var invalidTokenCodes = map[int]bool{
	20005:    true, // authen endpoints: access token invalid
	99991661: true, // expired
	99991662: true,
	99991663: true, // invalid access token
	99991664: true, // token/app type mismatch
	99991665: true,
	99991666: true,
	99991667: true,
	99991668: true, // invalid access token for authorization
	99991669: true,
}

// ValidateCredential probes every token the tool holds with type-matched
// endpoints. A probe is accepted when it returns code 0 or any non-auth
// business error (scope denial, field validation, 404 ...) — the gateway
// validated the token before producing those. Probes that fail with auth codes
// mark that token type invalid and disable it so later modules never use it.
// Returns an error when NO token can be validated (-> abort the run).
func (d *Dumper) ValidateCredential(ctx context.Context) error {
	if d.userToken == "" && d.appToken == "" && d.tenantToken == "" && !d.internal {
		return fmt.Errorf("no credential provided")
	}
	type probe struct {
		kind string // user | tenant | app
		spec CallSpec
	}
	var probes []probe
	if d.userToken != "" {
		probes = append(probes, probe{"user", CallSpec{Method: "GET", Path: "/open-apis/authen/v1/user_info", Supports: SupUser, Quiet: true}})
	}
	if d.tenantToken != "" || d.internal {
		probes = append(probes,
			probe{"tenant", CallSpec{Method: "GET", Path: "/open-apis/contact/v3/scopes", Supports: SupTenant, Quiet: true}},
		)
		if d.userToken == "" {
			probes = append(probes,
				probe{"tenant", CallSpec{Method: "GET", Path: "/open-apis/im/v1/chats", Query: larkcore.QueryParams{"page_size": {"1"}}, Supports: SupTenant | SupUser, Quiet: true}},
				probe{"tenant", CallSpec{Method: "GET", Path: "/open-apis/contact/v3/custom_attrs", Query: larkcore.QueryParams{"page_size": {"1"}}, Supports: SupTenant, Quiet: true}},
			)
		}
	}
	if d.appToken != "" {
		probes = append(probes,
			probe{"app", CallSpec{Method: "GET", Path: "/open-apis/contact/v3/scopes", Supports: SupApp, Quiet: true}},
		)
	}
	if len(probes) == 0 {
		return fmt.Errorf("no probe for the provided credential")
	}

	pass := map[string]bool{}
	fail := map[string]bool{}
	var lastErr error
	for _, p := range probes {
		_, err := d.do(ctx, p.spec)
		if err == nil {
			pass[p.kind] = true
			d.log("auth", fmt.Sprintf("%s credential valid (probe %s -> code 0)", p.kind, p.spec.Path))
			continue
		}
		var fe *FeishuError
		if errors.As(err, &fe) && !invalidTokenCodes[fe.Code] && fe.HTTP != 429 && fe.HTTP < 500 {
			// token cleared the gateway; the endpoint denied for business reasons
			pass[p.kind] = true
			d.log("auth", fmt.Sprintf("%s credential valid (probe %s -> %s)", p.kind, p.spec.Path, fe.Error()))
			continue
		}
		if errors.As(err, &fe) {
			fail[p.kind] = true
			lastErr = err
			d.reqLog("credential probe (%s) %s: %v", p.kind, p.spec.Path, err)
			continue
		}
		// transport-level failure: don't disable the token, just remember it
		lastErr = err
		d.reqLog("credential probe (%s) %s: %v", p.kind, p.spec.Path, err)
	}

	// disable token kinds that failed auth so nothing else tries them
	if fail["user"] && d.userToken != "" {
		d.log("auth", "user_access_token failed auth and was disabled")
		d.userToken = ""
	}
	if fail["tenant"] && d.tenantToken != "" {
		d.log("auth", "tenant_access_token failed auth and was disabled")
		d.tenantToken = ""
	}
	if fail["app"] && d.appToken != "" {
		d.log("auth", "app_access_token failed auth and was disabled")
		d.appToken = ""
	}

	if len(pass) == 0 {
		return fmt.Errorf("no usable credential (probe error: %w)", lastErr)
	}
	return nil
}

// ForEach runs fn(i) for every index 0..n-1 across up to `workers` goroutines.
// Indices are stable (results can be written into pre-sized slices).
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

// noteErrorCode tallies business error codes for the meta summary.
func (d *Dumper) noteErrorCode(code int) {
	if code == 0 {
		return
	}
	d.errMu.Lock()
	if d.errCodes == nil {
		d.errCodes = map[int]int{}
	}
	d.errCodes[code]++
	d.errMu.Unlock()
}

// ErrorCodeSummary returns a snapshot of the business-error tally.
func (d *Dumper) ErrorCodeSummary() map[string]int {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	out := map[string]int{}
	for k, v := range d.errCodes {
		out[fmt.Sprintf("%d", k)] = v
	}
	return out
}

// ---- per-endpoint rate-limit budget handling -------------------------------

// staticWords are path segments that stay verbatim when normalizing an API path
// into a rate-limit bucket key; anything else that looks like an id becomes "*".
var staticWords = map[string]bool{
	"open-apis": true, "v1": true, "v2": true, "v3": true, "v4": true, "v6": true,
	"contact": true, "contacts": true, "im": true, "calendar": true, "drive": true,
	"docx": true, "sheets": true, "bitable": true, "wiki": true, "task": true,
	"approval": true, "openapi": true, "mail": true, "minutes": true, "okr": true,
	"hire": true, "corehr": true, "authen": true, "application": true, "tenant": true,
	"bot": true, "info": true, "users": true, "user_info": true, "scopes": true,
	"departments": true, "children": true, "group": true, "simplelist": true,
	"member": true, "custom_attrs": true, "employee_type_enums": true,
	"job_families": true, "job_levels": true, "work_cities": true, "unit": true,
	"chats": true, "messages": true, "batch_messages": true, "images": true,
	"managers": true, "announcement": true, "pins": true, "reactions": true,
	"resources": true, "calendars": true, "primary": true, "primarys": true,
	"events": true, "attendees": true, "acls": true, "files": true, "folders": true,
	"media": true, "medias": true, "metas": true, "batch_query": true,
	"permissions": true, "public": true, "members": true, "comments": true,
	"documents": true, "raw_content": true, "blocks": true, "convert": true,
	"spreadsheets": true, "metainfo": true, "values_batch_get": true, "values": true,
	"apps": true, "tables": true, "records": true, "fields": true, "views": true,
	"spaces": true, "nodes": true, "get_node": true, "search": true, "query": true,
	"list": true, "get": true, "applications": true,
	"instances": true, "instance": true, "detail": true,
	"mailgroups": true, "public_mailboxes": true, "user_mailboxes": true,
	"transcript": true, "statistics": true, "artifacts": true, "periods": true,
	"employees": true, "tasks": true, "tasklists": true, "subtasks": true,
	"attachments": true, "download": true, "export_tasks": true, "import_tasks": true,
	"view_records": true, "versions": true, "replies": true,
	"subscription": true, "unsubscription": true, "favorites": true,
}

func normalizePath(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		if staticWords[s] || s == "" {
			continue
		}
		if looksLikeID(s) {
			segs[i] = "*"
		}
	}
	return strings.Join(segs, "/")
}

func looksLikeID(s string) bool {
	if s == "" {
		return false
	}
	if allDigits(s) {
		return true
	}
	// ids/tokens: long opaque strings, u-/t- tokens, or prefixed ids (oc_/om_/...)
	if len(s) >= 16 {
		return true
	}
	if strings.HasPrefix(s, "u-") || strings.HasPrefix(s, "t-") {
		return true
	}
	for _, p := range []string{"oc_", "om_", "ou_", "od_", "og_", "on_", "cl_", "ex_", "st_", "ct_"} {
		if strings.HasPrefix(s, p) && len(s) >= 10 {
			return true
		}
	}
	return len(s) >= 8 && strings.ContainsAny(s, "_-") && !staticWords[s]
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// noteBudget records the rate-limit counters from a response header.
func (d *Dumper) noteBudget(norm string, h http.Header) {
	remaining, hasRemaining := headerInt(h, "-remaining")
	reset, hasReset := headerTs(h, "-reset")
	if !hasRemaining && !hasReset {
		return
	}
	d.budgeMu.Lock()
	defer d.budgeMu.Unlock()
	b := d.budgets[norm]
	if b == nil {
		b = &endpointBudget{}
		d.budgets[norm] = b
	}
	if hasRemaining {
		b.remaining = remaining
		b.has = true
	}
	if hasReset {
		b.reset = reset
	}
}

// waitBudget sleeps when the last response for this endpoint showed the budget
// nearly exhausted (remaining <= 1): individual APIs refill on their own reset
// schedule, which the server communicates per-response.
func (d *Dumper) waitBudget(ctx context.Context, norm string) {
	d.budgeMu.Lock()
	b := d.budgets[norm]
	d.budgeMu.Unlock()
	if b == nil || !b.has || b.remaining > 1 {
		return
	}
	dur := 1500 * time.Millisecond
	if b.reset.After(time.Now()) {
		dur = time.Until(b.reset) + 100*time.Millisecond
	}
	if dur > 120*time.Second {
		dur = 120 * time.Second
	}
	if dur <= 0 {
		return
	}
	fmt.Printf("[ratelimit] %s remaining=%d, sleep %s\n", norm, b.remaining, dur.Round(time.Millisecond))
	d.reqLog("[ratelimit] %s remaining=%d, sleep %s", norm, b.remaining, dur.Round(time.Millisecond))
	select {
	case <-ctx.Done():
		return
	case <-time.After(dur):
	}
}

func (d *Dumper) sleepRetryAfter(ctx context.Context, h http.Header) {
	v := h.Get("Retry-After")
	if v == "" {
		return
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		if secs > 120 {
			secs = 120
		}
		fmt.Printf("[ratelimit] Retry-After=%ds, sleep\n", secs)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(secs) * time.Second):
		}
		return
	}
	if t, err := http.ParseTime(v); err == nil {
		dur := time.Until(t)
		if dur > 0 && dur < 2*time.Minute {
			fmt.Printf("[ratelimit] Retry-After=%s, sleep %s\n", v, dur.Round(time.Millisecond))
			select {
			case <-ctx.Done():
				return
			case <-time.After(dur):
			}
		}
	}
}

// headerInt looks for a header whose (lowercased) name ends with suffix and
// returns its integer value.
func headerInt(h http.Header, suffix string) (int, bool) {
	for k, vs := range h {
		if len(vs) == 0 {
			continue
		}
		if strings.HasSuffix(strings.ToLower(k), suffix) {
			n, err := strconv.Atoi(strings.TrimSpace(vs[0]))
			if err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// headerTs looks for a header whose name ends with "-reset"; large values are
// unix seconds, small values are relative seconds from now.
func headerTs(h http.Header, suffix string) (time.Time, bool) {
	n, ok := headerInt(h, suffix)
	if !ok || n <= 0 {
		return time.Time{}, false
	}
	if n > 1_000_000_000 {
		return time.Unix(int64(n), 0), true
	}
	return time.Now().Add(time.Duration(n) * time.Second), true
}
