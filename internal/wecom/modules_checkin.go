package wecom

import (
	"context"
	"fmt"
	"time"
)

func checkinTasks() []task {
	g := "checkin"
	return []task{
		{Group: g, Key: "options", Desc: "成员打卡规则", Run: modCheckinOptions},
		{Group: g, Key: "day", Desc: "打卡日报数据(近90天)", Run: modCheckinDay},
		{Group: g, Key: "month", Desc: "打卡月报数据(近90天)", Run: modCheckinMonth},
	}
}

// checkinUserIDs: 打卡接口按成员查询，优先全员；无通讯录权限则无法枚举。
func (d *Dumper) checkinUserIDs(ctx context.Context) ([]string, error) {
	uids := d.userIDs(ctx)
	if len(uids) == 0 {
		return nil, fmt.Errorf("no userids available for checkin APIs")
	}
	if d.opt.MaxItems > 0 && len(uids) > d.opt.MaxItems {
		uids = uids[:d.opt.MaxItems]
	}
	return uids, nil
}

// userBatches splits userids into chunks acceptable by the checkin APIs.
func userBatches(uids []string, size int) [][]string {
	var out [][]string
	for i := 0; i < len(uids); i += size {
		end := i + size
		if end > len(uids) {
			end = len(uids)
		}
		out = append(out, uids[i:end])
	}
	return out
}

func modCheckinOptions(ctx context.Context, d *Dumper) (interface{}, error) {
	uids, err := d.checkinUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	var all []interface{}
	now := time.Now().UnixMilli()
	batches := userBatches(uids, 100)
	for _, b := range batches {
		if ctx.Err() != nil {
			break
		}
		root, err := d.postJSON(ctx, "/checkin/getcheckinoption", map[string]interface{}{
			"datetime": now, "useridlist": b,
		})
		if err != nil {
			d.reqLog("getcheckinoption batch(%d): %v", len(b), err)
			continue
		}
		all = append(all, arrOf(root["infos"])...)
	}
	return all, nil
}

// checkinWindow 拉一个时间窗口的日报/月报数据（接口跨度上限 30 天）。
func (d *Dumper) checkinWindow(ctx context.Context, path, name string, uids []string, days int) []interface{} {
	var all []interface{}
	end := time.Now()
	for w := 0; w*30 < days; w++ {
		if ctx.Err() != nil {
			break
		}
		start := end.AddDate(0, 0, -30)
		sMs, eMs := start.UnixMilli(), end.UnixMilli()
		for _, b := range userBatches(uids, 100) {
			if ctx.Err() != nil {
				break
			}
			root, err := d.postJSON(ctx, path, map[string]interface{}{
				"starttime": sMs, "endtime": eMs, "useridlist": b,
			})
			if err != nil {
				d.reqLog("%s window %d batch(%d): %v", name, w, len(b), err)
				continue
			}
			all = append(all, arrOf(root["datas"])...)
		}
		end = start
	}
	return all
}

func modCheckinDay(ctx context.Context, d *Dumper) (interface{}, error) {
	uids, err := d.checkinUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	return d.checkinWindow(ctx, "/checkin/getcheckinday", "getcheckinday", uids, 90), nil
}

func modCheckinMonth(ctx context.Context, d *Dumper) (interface{}, error) {
	uids, err := d.checkinUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	return d.checkinWindow(ctx, "/checkin/getcheckinmonth", "getcheckinmonth", uids, 90), nil
}

func arrOf(v interface{}) []interface{} {
	a, _ := v.([]interface{})
	return a
}
