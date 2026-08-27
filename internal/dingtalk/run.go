package dingtalk

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TaskInfo describes one dump module for the CLI `list` command.
type TaskInfo struct {
	ID    string
	Desc  string
	Group string
}

// DetectCredential 判断松散凭据是否属于钉钉（kd run 自动识别用）。
func DetectCredential(appID, token string) bool {
	switch {
	case strings.HasPrefix(appID, "ding"):
		return true
	case token != "" && !strings.HasPrefix(token, "t-") && !strings.HasPrefix(token, "u-") && !strings.HasPrefix(appID, "cli_"):
		return true
	}
	return false
}

// ListTasks returns every module id/description in execution order.
func ListTasks() []TaskInfo {
	tasks := allTasks()
	out := make([]TaskInfo, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, TaskInfo{ID: t.id(), Desc: t.Desc, Group: t.Group})
	}
	return out
}

func allTasks() []task {
	var tasks []task
	tasks = append(tasks, approvalTasks()...)
	tasks = append(tasks, contactTasks()...)
	tasks = append(tasks, attendanceTasks()...)
	tasks = append(tasks, miscTasks()...)
	return tasks
}

// Run drives the full dump pipeline.
func (d *Dumper) Run(ctx context.Context) error {
	outDir := d.opt.Out
	if outDir == "" {
		outDir = fmt.Sprintf("dingtalk_dump_%s", time.Now().Format("20060102_150405"))
	}
	if strings.HasSuffix(outDir, ".json") {
		outDir = strings.TrimSuffix(outDir, ".json")
	}
	d.out = newOutput(outDir)
	o := d.out

	switch {
	case d.opt.AppSecret != "" && d.opt.AccessToken != "":
		o.SetMeta("auth_method", "appkey+appsecret (direct token fallback)")
	case d.opt.AppSecret != "":
		o.SetMeta("auth_method", "appkey+appsecret")
	default:
		o.SetMeta("auth_method", "access_token")
	}

	fmt.Println("== credential check ==")
	if err := d.ValidateCredential(ctx); err != nil {
		o.SetMeta("auth_error", err.Error())
		_ = o.WriteMeta(false)
		return fmt.Errorf("credential check failed: %w", err)
	}

	fmt.Println("== identity ==")
	d.identity(ctx)
	// SDK 首选的权限接口：企业应用授权信息
	if auth, err := d.OrgAuthInfo(ctx); err == nil {
		o.SetMeta("org_auth", auth)
		d.log("identity", fmt.Sprintf("org auth info captured (%d fields)", len(auth)))
	} else {
		d.reqLog("org_auth(sdk): %v", err)
	}
	_ = o.WriteMeta(false)

	fmt.Println("== data dump ==")
	start := time.Now()
	stats := map[string]map[string]int{}
	perf := map[string]float64{}
	for _, t := range allTasks() {
		if ctx.Err() != nil {
			break
		}
		id := t.id()
		if d.opt.Skip[id] {
			d.log(id, "skipped")
			continue
		}
		if len(d.opt.Only) > 0 && !d.opt.Only[id] {
			continue
		}
		fmt.Printf("— %s %s\n", id, t.Desc)
		t0 := time.Now()
		v, err := t.Run(ctx, d)
		if err != nil {
			d.log(id, "error: "+err.Error())
			o.Logf("%s: error: %v", id, err)
			continue
		}
		written := false
		switch val := v.(type) {
		case []interface{}:
			if len(val) == 0 {
				d.log(id, "empty, no file written")
				o.Logf("%s: empty result, no file written", id)
			} else {
				fmt.Printf("  %s: %d items\n", id, len(val))
				o.WriteTask(t, val)
				written = true
			}
		case map[string]interface{}:
			if len(val) == 0 {
				d.log(id, "empty, no file written")
				o.Logf("%s: empty result, no file written", id)
			} else {
				fmt.Printf("  %s: %d entries\n", id, len(val))
				o.WriteTask(t, val)
				written = true
			}
		default:
			o.WriteTask(t, v)
			written = true
		}
		if stats[t.Group] == nil {
			stats[t.Group] = map[string]int{}
		}
		perf[id] = time.Since(t0).Seconds()
		if !written {
			continue
		}
		switch val := v.(type) {
		case []interface{}:
			stats[t.Group][t.Key] = len(val)
		case map[string]interface{}:
			stats[t.Group][t.Key] = len(val)
		}
	}

	// resources stage
	records := d.DownloadResources(ctx)
	ok := 0
	for _, r := range records {
		if r.Error != "" {
			o.Logf("resource %s/%s: %s", r.Source, r.ID, r.Error)
			continue
		}
		ok++
	}
	if ok > 0 {
		o.WriteResources(records)
		fmt.Printf("  resources: %d downloaded, %d failed\n", ok, len(records)-ok)
	} else if len(records) > 0 {
		o.Logf("resources: 0/%d succeeded, no file written", len(records))
	}

	o.SetMeta("stats", stats)
	o.SetMeta("perf", perf)
	o.SetMeta("request_codes", d.ErrorCodeSummary())
	o.SetMeta("total_requests", d.reqCnt.Load())

	fmt.Printf("\nrequests: %d, elapsed: %s\n", d.reqCnt.Load(), time.Since(start).Round(time.Second))
	if codes := d.ErrorCodeSummary(); len(codes) > 0 {
		fmt.Printf("error codes: %v\n", codes)
	}
	final := ctx.Err() == nil
	if err := o.WriteMeta(final); err != nil {
		return err
	}
	fmt.Printf("saved: %s/\n", o.Dir())
	fmt.Printf("log  : %s\n", o.LogPath())
	return nil
}
