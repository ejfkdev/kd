package feishu

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// task is one data dump step, written to <out>/<Group>/<Key>.json.
// The full module id used by -only/-skip is "<Group>.<Key>".
type task struct {
	Group     string
	Key       string
	Desc      string
	Needs     Supports // required access token kind; 0 = whatever is available
	File      string   // overrides the plain (non-split) output file name
	Footprint string   // base dir of split output (used by -resume existence check)
	Run       func(ctx context.Context, d *Dumper) (interface{}, error)
}

func (t task) id() string { return t.Group + "." + t.Key }

// tokenOK reports whether the credential can satisfy `needs` (any bit).
func (d *Dumper) tokenOK(needs Supports) bool {
	if needs == 0 {
		return true
	}
	if needs&SupUser != 0 && d.userToken != "" {
		return true
	}
	if needs&SupTenant != 0 && (d.tenantToken != "" || d.internal) {
		return true
	}
	if needs&SupApp != 0 && (d.appToken != "" || d.internal) {
		return true
	}
	return false
}

func tokenDesc(needs Supports) string {
	parts := []string{}
	if needs&SupUser != 0 {
		parts = append(parts, "user_access_token")
	}
	if needs&SupTenant != 0 {
		parts = append(parts, "tenant_access_token")
	}
	if needs&SupApp != 0 {
		parts = append(parts, "app_access_token")
	}
	return strings.Join(parts, "+")
}

// TaskInfo describes one dump module for the CLI `list` command.
type TaskInfo struct {
	ID    string
	Desc  string
	Group string
}

// DetectCredential 判断松散凭据是否属于飞书（kd run 自动识别用）。
func DetectCredential(appID, token string) bool {
	switch {
	case strings.HasPrefix(appID, "cli_"):
		return true
	case strings.HasPrefix(token, "t-"), strings.HasPrefix(token, "u-"):
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

// allTasks is the dump pipeline, executed in this order.
func allTasks() []task {
	// im runs first: its messages harvest user/chat/doc ids that later
	// modules use as fallback when their list APIs are not accessible
	var tasks []task
	tasks = append(tasks, imTasks()...)
	tasks = append(tasks, contactTasks()...)
	tasks = append(tasks, calendarTasks()...)
	tasks = append(tasks, docsTasks()...)
	tasks = append(tasks, miscTasks()...)
	return tasks
}

func (d *Dumper) Run(ctx context.Context) error {
	outDir := d.opt.Out
	if outDir == "" {
		outDir = fmt.Sprintf("feishu_dump_%s", time.Now().Format("20060102_150405"))
	}
	// tolerate a legacy *.json path: use it as the directory base name
	if strings.HasSuffix(outDir, ".json") {
		outDir = strings.TrimSuffix(outDir, ".json")
	}
	d.out = newOutput(outDir)
	o := d.out

	// auth summary
	switch {
	case d.opt.Code != "":
		o.setMeta("auth_method", "authorization_code")
	case d.userToken != "" && d.internal:
		o.setMeta("auth_method", "app_secret + user_access_token")
	case d.userToken != "":
		o.setMeta("auth_method", string(ModeUserDirect))
	case d.tenantToken != "":
		o.setMeta("auth_method", string(ModeTenantDir))
	case d.appToken != "":
		o.setMeta("auth_method", string(ModeAppDirect))
	default:
		o.setMeta("auth_method", string(ModeInternal))
	}
	o.setMeta("host", d.opt.Host)
	o.setMeta("app_id", d.opt.AppID)
	o.setMeta("has_user_token", d.userToken != "")
	o.setMeta("has_tenant_token", d.tenantToken != "" || d.internal)
	o.setMeta("target_user_id_type", d.opt.UserIDType)
	if d.opt.RefreshToken != "" {
		o.setMeta("refresh_token", d.opt.RefreshToken)
	}

	fmt.Println("== credential check ==")
	if err := d.ValidateCredential(ctx); err != nil {
		o.setMeta("auth_error", err.Error())
		_ = o.WriteMeta(false)
		return fmt.Errorf("credential check failed: %w", err)
	}

	fmt.Println("== identity ==")
	d.identity(ctx)
	if err := o.WriteMeta(false); err != nil {
		return err
	}

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
		if d.opt.Resume && o.TaskDone(t) {
			d.log(id, "already exists, skipped (-resume)")
			d.reqLog("%s: skipped, output already exists (-resume)", id)
			continue
		}
		if !d.tokenOK(t.Needs) {
			d.log(id, "skipped: token mismatch (needs "+tokenDesc(t.Needs)+")")
			d.reqLog("%s: skipped (token mismatch, needs %s)", id, tokenDesc(t.Needs))
			continue
		}
		v, err := t.Run(ctx, d)
		if err != nil {
			// errors never land in data json: they go to run.log only
			d.log(id, "error: "+err.Error())
			o.Logf("%s: error: %v", id, err)
			continue
		}
		if sr, ok := v.(*SplitResult); ok {
			total := 0
			for _, p := range sr.Parts {
				if len(p.Object) > 0 {
					total++
				} else {
					total += len(p.Items)
				}
			}
			fmt.Printf("  %s: %d items / %d files\n", id, total, len(sr.Parts))
			if err := o.WriteTask(t, sr); err != nil {
				return err
			}
			if stats[t.Group] == nil {
				stats[t.Group] = map[string]int{}
			}
			stats[t.Group][t.Key] = total
			notePerf(perf, t, t0)
			continue
		}
		if arr, ok := v.([]interface{}); ok {
			if len(arr) == 0 {
				d.log(id, "empty, no file written")
				o.Logf("%s: empty result, no file written", id)
				continue
			}
			fmt.Printf("  %s: %d items\n", id, len(arr))
			if err := o.WriteTask(t, arr); err != nil {
				return err
			}
			if stats[t.Group] == nil {
				stats[t.Group] = map[string]int{}
			}
			stats[t.Group][t.Key] = len(arr)
			notePerf(perf, t, t0)
			continue
		}
		if m, ok := v.(map[string]interface{}); ok {
			if len(m) == 0 || mapHasNoData(m) {
				d.log(id, "empty, no file written")
				o.Logf("%s: empty result, no file written", id)
				continue
			}
			fmt.Printf("  %s: %d entries\n", id, len(m))
			if err := o.WriteTask(t, m); err != nil {
				return err
			}
			if stats[t.Group] == nil {
				stats[t.Group] = map[string]int{}
			}
			stats[t.Group][t.Key] = len(m)
			notePerf(perf, t, t0)
			continue
		}
		if err := o.WriteTask(t, v); err != nil {
			return err
		}
		notePerf(perf, t, t0)
	}

	// final stage: download every discovered resource (attachments/images)
	fmt.Println("— resources: 下载资源")
	if ctx.Err() != nil {
		if err := o.WriteMeta(false); err != nil {
			return err
		}
		return ctx.Err()
	}
	records := d.downloadResources(ctx)
	ok := 0
	for _, r := range records {
		if r.Error != "" {
			o.Logf("resource %s/%s: %s", r.Source, r.ID, r.Error)
			continue
		}
		ok++
	}
	if ok > 0 {
		if err := o.WriteResources(records); err != nil {
			return err
		}
		fmt.Printf("  resources: %d downloaded, %d failed\n", ok, len(records)-ok)
	} else {
		o.Logf("resources: 0/%d succeeded, no file written", len(records))
		fmt.Printf("  resources: 0/%d succeeded (see run.log)\n", len(records))
	}
	stats["resources"] = map[string]int{"downloaded_files": ok, "failed": len(records) - ok}
	o.setMeta("stats", stats)

	final := ctx.Err() == nil
	o.setMeta("request_codes", d.ErrorCodeSummary())
	o.setMeta("perf", perf)
	o.setMeta("total_requests", d.reqCnt.Load())
	fmt.Printf("\nrequests: %d, elapsed: %s\n", d.reqCnt.Load(), time.Since(start).Round(time.Second))
	if codes := d.ErrorCodeSummary(); len(codes) > 0 {
		fmt.Printf("error codes: %v\n", codes)
	}
	if err := o.WriteMeta(final); err != nil {
		return err
	}
	fmt.Printf("saved: %s/\n", o.Dir())
	fmt.Printf("  meta.json / <业务>/<模块>.json / resources/resources.json + 资源文件\n")
	fmt.Printf("log  : %s\n", o.LogPath())
	return nil
}

// mapHasNoData reports whether a module's map result carries only empty
// containers (empty lists/maps/strings) — such results produce no file.
func mapHasNoData(m map[string]interface{}) bool {
	for _, v := range m {
		switch vv := v.(type) {
		case nil:
		case []interface{}:
			if len(vv) > 0 {
				return false
			}
		case map[string]interface{}:
			if len(vv) > 0 {
				return false
			}
		case string:
			if vv != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// notePerf records a task's wall-clock seconds for the meta perf map.
func notePerf(perf map[string]float64, t task, t0 time.Time) {
	perf[t.id()] = time.Since(t0).Seconds()
}
