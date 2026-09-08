package wecom

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

func approvalTasks() []task {
	g := "approval"
	return []task{
		{Group: g, Key: "templates", Desc: "审批应用表单模板", Run: modApprovalTemplates},
		{Group: g, Key: "instances", Desc: "审批单号列表(近一年)", Run: modApprovalInstances},
		{Group: g, Key: "instance_details", Desc: "审批单详情", Run: modApprovalDetails},
	}
}

// modApprovalTemplates: POST /oa/dialist 返回审批应用的表单模板。
func modApprovalTemplates(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("approval.templates", func() (interface{}, error) {
		root, err := d.postJSON(ctx, "/oa/dialist", map[string]interface{}{})
		if err != nil {
			return nil, err
		}
		return pickAny(root, "dia_list"), nil
	})
}

// modApprovalInstances: /oa/getapprovalinfo 按月窗口枚举近一年的审批单号。
func modApprovalInstances(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("approval.instances", func() (interface{}, error) {
		var all []interface{}
		seen := map[string]bool{}
		end := time.Now()
		for w := 0; w < 12; w++ {
			if ctx.Err() != nil {
				break
			}
			start := end.AddDate(0, -1, 0)
			wStart, wEnd := strconv.FormatInt(start.Unix(), 10), strconv.FormatInt(end.Unix(), 10)
			items, err := d.CollectPages(ctx, PageSpec{
				Name: "approval_instances", Path: "/oa/getapprovalinfo",
				Body: func(cursor string, size int) map[string]interface{} {
					c, _ := strconv.Atoi(cursor)
					return map[string]interface{}{
						"starttime": wStart, "endtime": wEnd,
						"cursor": c, "size": size, "filters": []interface{}{},
					}
				},
				ListPath: "sp_no_list", NextPath: "next_cursor", Size: 100, MaxItems: d.opt.MaxItems,
			})
			if err != nil {
				d.reqLog("getapprovalinfo window %d: %v", w, err)
				if w == 0 {
					return nil, err
				}
			}
			for _, it := range items {
				if s := strVal(it); s != "" && !seen[s] {
					seen[s] = true
					all = append(all, it)
				}
			}
			end = start
		}
		return all, nil
	})
}

func modApprovalDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	raw, err := modApprovalInstances(ctx, d)
	if err != nil {
		return nil, err
	}
	list, _ := raw.([]interface{})
	ids := make([]string, 0, len(list))
	for _, it := range list {
		if s := strVal(it); s != "" {
			ids = append(ids, s)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no approval sp_no available")
	}
	res := make([]interface{}, len(ids))
	d.ForEach(ctx, d.opt.Workers, len(ids), func(i int) {
		if ctx.Err() != nil {
			return
		}
		root, err := d.postJSON(ctx, "/oa/getapproaldetail", map[string]interface{}{"sp_no": ids[i]})
		if err != nil {
			d.reqLog("getapproaldetail %s: %v", ids[i], err)
			return
		}
		res[i] = root["info"]
	})
	out := map[string]interface{}{}
	for i := range ids {
		if res[i] != nil {
			out[ids[i]] = res[i]
		}
	}
	return out, nil
}
