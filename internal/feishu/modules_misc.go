package feishu

import (
	"context"
	"fmt"
	"sync/atomic"
)

func miscTasks() []task {
	return []task{
		{Group: "task", Key: "tasks", Desc: "任务", Needs: SupUser, Run: modTasks},
		{Group: "task", Key: "comments", Desc: "任务评论", Needs: SupUser, Run: modTaskComments},
		{Group: "task", Key: "attachments", Desc: "任务附件抽取", Needs: SupUser, Run: modTaskAttachments},
		{Group: "approval", Key: "instances", Desc: "审批实例", Run: modApprovalInstances},
		{Group: "approval", Key: "instance_details", Desc: "审批实例详情", Run: modApprovalDetails},
		{Group: "approval", Key: "comments", Desc: "审批评论", Run: modApprovalComments},
		{Group: "mail", Key: "groups", Desc: "邮件组", Run: modMailGroups},
		{Group: "mail", Key: "group_members", Desc: "邮件组成员", Run: modMailGroupMembers},
		{Group: "mail", Key: "public_mailboxes", Desc: "公共邮箱", Run: modMailPublicMailboxes},
		{Group: "mail", Key: "public_mailbox_members", Desc: "公共邮箱成员", Run: modMailPublicMailboxMembers},
		{Group: "mail", Key: "user_mailboxes", Desc: "成员邮箱", Run: modMailUserMailboxes},
		{Group: "minutes", Key: "list", Desc: "妙记", Needs: SupUser, Run: modMinutes},
		{Group: "minutes", Key: "transcripts", Desc: "妙记转写", Needs: SupUser, Run: modMinutesTranscripts},
		{Group: "minutes", Key: "artifacts", Desc: "妙记素材抽取", Needs: SupUser, Run: modMinutesArtifacts},
		{Group: "okr", Key: "periods", Desc: "OKR周期", Needs: SupUser, Run: modOkrPeriods},
		{Group: "hr", Key: "hire_employees", Desc: "招聘人员(hire)", Run: modHireEmployees},
		{Group: "hr", Key: "corehr_employees", Desc: "人事人员(corehr)", Run: modCorehrEmployees},
		{Group: "misc", Key: "bot_info", Desc: "机器人信息", Run: modBotInfo},
	}
}

func modTasks(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{
		Name: "task_tasks", Method: "GET", Path: "/open-apis/task/v2/tasks",
		Query:    uidTypeQ(d),
		Supports: SupUser, PageSize: 100,
	})
}

func modTaskComments(ctx context.Context, d *Dumper) (interface{}, error) {
	tasks, err := modTasks(ctx, d)
	if err != nil {
		return nil, err
	}
	arr, _ := tasks.([]interface{})
	all := []interface{}{}
	for _, t := range arr {
		if ctx.Err() != nil {
			break
		}
		m, _ := t.(map[string]interface{})
		guid := m["guid"]
		if guid == nil || strVal(guid) == "" {
			continue
		}
		q := uidTypeQ(d)
		q.Set("resource_type", "task")
		q.Set("resource_id", strVal(guid))
		items, err := d.Collect(ctx, ListConf{
			Name: "task_comments", Method: "GET", Path: "/open-apis/task/v2/comments",
			Query:    q,
			Supports: SupUser | SupTenant, PageSize: 100,
		})
		if err != nil {
			continue
		}
		all = append(all, attach(items, "task_guid", strVal(guid))...)
	}
	return all, nil
}

// approval instance list pages with body params instead of query params.
// modApprovalInstances: approval v4 list endpoint (openapi/v2 was retired).
// modApprovalInstances: approval v4 — the plain GET list requires an
// approval_code, so enumerate via POST /instances/query with an empty filter.
func modApprovalInstances(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("approval.instances", func() (interface{}, error) {
		return d.Collect(ctx, ListConf{
			Name: "approval_instances", Method: "POST", Path: "/open-apis/approval/v4/instances/query",
			Body:     func(pageToken string, pageNum int) interface{} { return map[string]interface{}{} },
			Query:    uidTypeQ(d),
			Supports: SupTenant, PageSize: 100, ListKey: "instance_list",
		})
	})
}

// approvalCode extracts the instance code from a v4 instance_list item.
func approvalCode(m map[string]interface{}) string {
	if inst, ok := m["instance"].(map[string]interface{}); ok {
		if c := strVal(inst["code"]); c != "" {
			return c
		}
	}
	return firstNonEmpty(strVal(m["instance_id"]), strVal(m["code"]))
}

// modApprovalDetails: one detail object per instance code, fetched in parallel.
func modApprovalDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	instsRaw, err := modApprovalInstances(ctx, d)
	if err != nil {
		return nil, err
	}
	insts, _ := instsRaw.([]interface{})
	codes := make([]string, len(insts))
	for i, it := range insts {
		m, _ := it.(map[string]interface{})
		codes[i] = approvalCode(m)
	}
	res := make([]interface{}, len(codes))
	d.ForEach(ctx, d.opt.Workers, len(codes), func(i int) {
		if ctx.Err() != nil || codes[i] == "" {
			return
		}
		m, err := d.CollectObj(ctx, "approval_instance_details", "GET", "/open-apis/approval/v4/instances/"+codes[i], nil, nil, SupTenant)
		if err != nil {
			d.reqLog("approval instance %s: %v", codes[i], err)
			return
		}
		res[i] = m
	})
	out := map[string]interface{}{}
	for i := range codes {
		if res[i] != nil {
			out[codes[i]] = res[i]
		}
	}
	return out, nil
}

// modApprovalComments: per-instance comment list (v4).
func modApprovalComments(ctx context.Context, d *Dumper) (interface{}, error) {
	instsRaw, err := modApprovalInstances(ctx, d)
	if err != nil {
		return nil, err
	}
	insts, _ := instsRaw.([]interface{})
	codes := make([]string, len(insts))
	for i, it := range insts {
		m, _ := it.(map[string]interface{})
		codes[i] = approvalCode(m)
	}
	res := make([][]interface{}, len(codes))
	var fails int64
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	d.ForEach(ctx2, d.opt.Workers, len(codes), func(i int) {
		if ctx2.Err() != nil || codes[i] == "" {
			return
		}
		q := uidTypeQ(d)
		items, err := d.Collect(ctx2, ListConf{
			Name: "approval_comments", Method: "GET",
			Path:     "/open-apis/approval/v4/instances/" + codes[i] + "/comments",
			Query:    q,
			Supports: SupUser | SupTenant, PageSize: 100, ListKey: "comments",
		})
		if err != nil {
			if atomic.AddInt64(&fails, 1) >= 5 {
				cancel()
			}
			d.reqLog("approval comments %s: %v", codes[i], err)
			return
		}
		res[i] = items
	})
	out := map[string]interface{}{}
	for i := range codes {
		if len(res[i]) > 0 {
			out[codes[i]] = res[i]
		}
	}
	return out, nil
}

func modMailGroups(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{
		Name: "mail_groups", Method: "GET", Path: "/open-apis/mail/v1/mailgroups",
		Supports: SupTenant, PageSize: 100,
	})
}

func modMailGroupMembers(ctx context.Context, d *Dumper) (interface{}, error) {
	groups, err := modMailGroups(ctx, d)
	if err != nil {
		return nil, err
	}
	arr, _ := groups.([]interface{})
	out := map[string]interface{}{}
	for _, g := range arr {
		if ctx.Err() != nil {
			break
		}
		m, _ := g.(map[string]interface{})
		gid := strVal(m["mailgroup_id"])
		if gid == "" {
			continue
		}
		items, err := d.Collect(ctx, ListConf{
			Name: "mail_group_members", Method: "GET",
			Path:     "/open-apis/mail/v1/mailgroups/" + gid + "/members",
			Supports: SupTenant, PageSize: 100,
		})
		if err != nil {
			d.reqLog("mailgroup %s members: %v", gid, err)
			continue
		}
		out[gid] = items
	}
	return out, nil
}

func modMailPublicMailboxes(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{
		Name: "mail_public_mailboxes", Method: "GET", Path: "/open-apis/mail/v1/public_mailboxes",
		Supports: SupTenant, PageSize: 100,
	})
}

func modMailPublicMailboxMembers(ctx context.Context, d *Dumper) (interface{}, error) {
	boxes, err := modMailPublicMailboxes(ctx, d)
	if err != nil {
		return nil, err
	}
	arr, _ := boxes.([]interface{})
	out := map[string]interface{}{}
	for _, b := range arr {
		if ctx.Err() != nil {
			break
		}
		m, _ := b.(map[string]interface{})
		bid := strVal(m["public_mailbox_id"])
		if bid == "" {
			continue
		}
		items, err := d.Collect(ctx, ListConf{
			Name: "mail_public_mailbox_members", Method: "GET",
			Path:     "/open-apis/mail/v1/public_mailboxes/" + bid + "/members",
			Supports: SupTenant, PageSize: 100,
		})
		if err != nil {
			d.reqLog("public mailbox %s members: %v", bid, err)
			continue
		}
		out[bid] = items
	}
	return out, nil
}

func modMailUserMailboxes(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{
		Name: "mail_user_mailboxes", Method: "GET", Path: "/open-apis/mail/v1/user_mailboxes",
		Supports: SupTenant, PageSize: 100,
	})
}

// modMinutes: the minutes list endpoint is POST /minutes/v1/minutes/search;
// an empty query returns the most recent minutes of the token's owner.
func modMinutes(ctx context.Context, d *Dumper) (interface{}, error) {
	items, err := d.Collect(ctx, ListConf{
		Name: "minutes_list", Method: "POST", Path: "/open-apis/minutes/v1/minutes/search",
		Body: func(pageToken string, pageNum int) interface{} {
			return map[string]interface{}{"query": ""}
		},
		Query:    uidTypeQ(d),
		Supports: SupUser | SupTenant, PageSize: 100,
	})
	if err != nil {
		// fallback: legacy GET list endpoint
		return d.Collect(ctx, ListConf{
			Name: "minutes_list", Method: "GET", Path: "/open-apis/minutes/v1/minutes",
			Supports: SupUser, PageSize: 20, ListKey: "minutes",
		})
	}
	return items, nil
}

func modMinutesTranscripts(ctx context.Context, d *Dumper) (interface{}, error) {
	minsRaw, err := modMinutes(ctx, d)
	if err != nil {
		return nil, err
	}
	mins, _ := minsRaw.([]interface{})
	out := map[string]interface{}{}
	for _, mi := range mins {
		if ctx.Err() != nil {
			break
		}
		m, _ := mi.(map[string]interface{})
		tok := strVal(m["token"])
		if tok == "" {
			tok = strVal(m["minute_token"])
		}
		if tok == "" {
			continue
		}
		entry := map[string]interface{}{}
		if tr, err := d.CollectObj(ctx, "minutes", "GET", "/open-apis/minutes/v1/minutes/"+tok+"/transcript", nil, nil, SupUser|SupTenant); err == nil {
			entry["transcript"] = tr
		} else {
			d.reqLog("minute %s transcript: %v", tok, err)
		}
		if st, err := d.CollectObj(ctx, "minutes", "GET", "/open-apis/minutes/v1/minutes/"+tok+"/statistics", nil, nil, SupUser|SupTenant); err == nil {
			entry["statistics"] = st
		} else {
			d.reqLog("minute %s statistics: %v", tok, err)
		}
		out[tok] = entry
	}
	return out, nil
}

func modOkrPeriods(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{
		Name: "okr_periods", Method: "GET", Path: "/open-apis/okr/v1/periods",
		Supports: SupUser, PageSize: 50,
	})
}

func modHireEmployees(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{
		Name: "hire_employees", Method: "GET", Path: "/open-apis/hire/v1/employees",
		Query:    uidTypeQ(d),
		Supports: SupTenant, PageSize: 100,
	})
}

func modCorehrEmployees(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{
		Name: "corehr_employees", Method: "POST", Path: "/open-apis/corehr/v2/employees/search",
		Body: func(pageToken string, pageNum int) interface{} {
			return map[string]interface{}{}
		},
		Query:    uidTypeQ(d),
		Supports: SupTenant, PageSize: 100,
	})
}

func modBotInfo(ctx context.Context, d *Dumper) (interface{}, error) {
	m, err := d.CollectObj(ctx, "bot_info", "GET", "/open-apis/bot/v3/info", nil, nil, SupTenant)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// modTaskAttachments lists every task attachment and queues it for download.
// Attachment objects carry guid + file_token + a 3-minute temp download url.
func modTaskAttachments(ctx context.Context, d *Dumper) (interface{}, error) {
	tasks, err := modTasks(ctx, d)
	if err != nil {
		return nil, err
	}
	arr, _ := tasks.([]interface{})
	all := []interface{}{}
	for _, t := range arr {
		if ctx.Err() != nil {
			break
		}
		m, _ := t.(map[string]interface{})
		guid := strVal(m["guid"])
		if guid == "" {
			continue
		}
		q := uidTypeQ(d)
		q.Set("resource_type", "task")
		q.Set("resource_id", guid)
		items, err := d.Collect(ctx, ListConf{
			Name: "task_attachments", Method: "GET", Path: "/open-apis/task/v2/attachments",
			Query:    q,
			Supports: SupUser | SupTenant, PageSize: 100,
		})
		if err != nil {
			continue
		}
		for _, it := range items {
			im, _ := it.(map[string]interface{})
			attID := strVal(im["guid"])
			if attID == "" {
				attID = strVal(im["id"])
			}
			d.AddResource("task", firstNonEmpty(attID, strVal(im["file_token"]), guid+"-"+strVal(im["name"])),
				strVal(im["name"]),
				map[string]interface{}{
					"url":        strVal(im["url"]),
					"file_token": strVal(im["file_token"]),
					"task_guid":  guid,
					"size":       im["size"],
				})
			nm := map[string]interface{}{"task_guid": guid}
			for k, v := range im {
				nm[k] = v
			}
			all = append(all, nm)
		}
	}
	return all, nil
}

// modMinutesArtifacts fetches (minute recordings, transcripts, videos) files
// for each minute and queues them for download.
func modMinutesArtifacts(ctx context.Context, d *Dumper) (interface{}, error) {
	minsRaw, err := modMinutes(ctx, d)
	if err != nil {
		return nil, err
	}
	mins, _ := minsRaw.([]interface{})
	out := []interface{}{}
	for _, mi := range mins {
		if ctx.Err() != nil {
			break
		}
		m, _ := mi.(map[string]interface{})
		tok := strVal(m["token"])
		if tok == "" {
			tok = strVal(m["minute_token"])
		}
		if tok == "" {
			continue
		}
		resp, err := d.do(ctx, CallSpec{
			Method: "GET", Path: "/open-apis/minutes/v1/minutes/" + tok + "/artifacts",
			Supports: SupUser | SupTenant,
		})
		if err != nil {
			continue
		}
		root, err := decodeMap(resp.RawBody)
		if err != nil {
			continue
		}
		data := subMap(root, "data")
		var found []interface{}
		for _, key := range []string{"files", "artifacts", "items"} {
			if fs, ok := data[key].([]interface{}); ok {
				found = fs
				break
			}
		}
		for i, f := range found {
			fm, _ := f.(map[string]interface{})
			url := strVal(fm["url"])
			if url == "" {
				url = strVal(fm["download_url"])
			}
			name := firstNonEmpty(strVal(fm["name"]), strVal(fm["file_name"]), strVal(fm["fileName"]))
			id := firstNonEmpty(strVal(fm["file_key"]), strVal(fm["key"]), strVal(fm["token"]), fmt.Sprintf("%s-%d", tok, i))
			if url == "" {
				continue
			}
			d.AddResource("minutes", id, name, map[string]interface{}{"url": url, "minute_token": tok})
			nm := map[string]interface{}{"minute_token": tok}
			for k, v := range fm {
				nm[k] = v
			}
			out = append(out, nm)
		}
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
