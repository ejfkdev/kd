package dingtalk

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

func approvalTasks() []task {
	g := "approval"
	return []task{
		{Group: g, Key: "instances", Desc: "审批实例id列表", Run: modApprovalInstanceIDs},
		{Group: g, Key: "instance_details", Desc: "审批实例详情", Run: modApprovalDetails},
	}
}

// modApprovalInstanceIDs: SDK-first (workflow_1_0.ListProcessInstanceIds),
// falling back to the legacy topapi listids endpoint when the SDK path fails.
func modApprovalInstanceIDs(ctx context.Context, d *Dumper) (interface{}, error) {
	// SDK 通道
	var ids []string
	start := time.Now().AddDate(-1, 0, 0).UnixMilli()
	end := time.Now().UnixMilli()
	var nextToken *int64
	sdkOK := false
	for page := 0; page < 100; page++ {
		batch, nxt, err := d.ListProcessInstanceIDs(ctx, 20, &start, &end, nextToken)
		if err != nil {
			if strings.Contains(err.Error(), "MissingprocessCode") || strings.Contains(err.Error(), "mandatory") {
				// SDK 要求 processCode，而当前没有任何已知流程 code（租户无表单亦会如此）：
				// 属平台约束而非失败，安静跳过。
				d.log("approval.instances", "no known process codes (SDK requires processCode): enumeration unavailable")
				return []interface{}{}, nil
			}
			d.reqLog("approval sdk list: %v (falling back to topapi)", err)
			break
		}
		sdkOK = true
		ids = append(ids, batch...)
		if nxt <= 0 {
			break
		}
		nextToken = &nxt
	}
	if sdkOK && len(ids) > 0 {
		return ids, nil
	}

	// topapi 回退
	return d.CollectPages(ctx, PageSpec{
		Name: "approval_instances", Path: "/topapi/processinstance/listids",
		Body: func(cursor int64, size int) map[string]interface{} {
			return map[string]interface{}{
				"process_code": "",
				"start_time":   start / 1000,
				"end_time":     end / 1000,
				"cursor":       cursor,
				"size":         size,
			}
		},
		Size: 20, ListPath: "result.list", NextPath: "result.next_cursor", MorePath: "result.has_more",
	})
}

func modApprovalDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	idsRaw, err := modApprovalInstanceIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	switch v := idsRaw.(type) {
	case []interface{}:
		for _, it := range v {
			ids = append(ids, strVal(it))
		}
	case []string:
		ids = v
	}
	res := make([]interface{}, len(ids))
	d.ForEach(ctx, d.opt.Workers, len(ids), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, err := d.GetProcessInstance(ctx, ids[i])
		if err != nil {
			d.reqLog("approval instance %s (sdk): %v", ids[i], err)
			// topapi 回退
			if data, err2 := d.do(ctx, callSpec{method: "POST", path: "/topapi/processinstance/get", body: map[string]interface{}{"process_instance_id": ids[i]}}); err2 == nil {
				var root map[string]interface{}
				if json.Unmarshal(data, &root) == nil {
					res[i] = root["process_instance"]
				}
				return
			}
			return
		}
		res[i] = m
	})
	out := map[string]interface{}{}
	for i := range ids {
		if res[i] != nil {
			out[ids[i]] = res[i]
		}
	}
	return out, nil
}
