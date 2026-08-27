package dingtalk

import (
	"context"
	"time"
)

func attendanceTasks() []task {
	g := "attendance"
	return []task{
		{Group: g, Key: "groups", Desc: "考勤组列表(SDK)", Run: modAttendanceGroups},
		{Group: g, Key: "checkin_records", Desc: "打卡记录(SDK, 逐用户)", Run: modCheckinRecords},
	}
}

// modAttendanceGroups: SDK-first; legacy topapi fallback for old-style apps.
func modAttendanceGroups(ctx context.Context, d *Dumper) (interface{}, error) {
	groups, _, err := d.AttendanceSimpleGroups(ctx, 100)
	if err != nil {
		d.reqLog("attendance groups (sdk): %v", err)
		// topapi 回退（老版应用类型）
		return d.CollectPages(ctx, PageSpec{
			Name: "attendance_groups", Path: "/topapi/attendance/group/list",
			Body: func(cursor int64, size int) map[string]interface{} {
				return map[string]interface{}{"offset": cursor, "size": size}
			},
			Size: 100, ListPath: "result.result", NextPath: "result.offset", MorePath: "result.has_more",
		})
	}
	var out []interface{}
	for _, g := range groups {
		out = append(out, g)
	}
	return out, nil
}

// modCheckinRecords: SDK checkin records for every known user over a window.
func modCheckinRecords(ctx context.Context, d *Dumper) (interface{}, error) {
	usersRaw, err := modUsers(ctx, d)
	if err != nil {
		return nil, err
	}
	users, _ := usersRaw.([]interface{})
	userIDs := make([]string, 0, len(users))
	for _, u := range users {
		if m, ok := u.(map[string]interface{}); ok {
			if uid := strVal(m["userid"]); uid != "" {
				userIDs = append(userIDs, uid)
			}
		}
	}
	if len(userIDs) == 0 {
		return []interface{}{}, nil
	}
	start := time.Now().AddDate(0, 0, -30).UnixMilli()
	end := time.Now().UnixMilli()
	res := make([][]interface{}, len(userIDs))
	d.ForEach(ctx, d.opt.Workers, len(userIDs), func(i int) {
		if ctx.Err() != nil {
			return
		}
		recs, _, err := d.CheckinRecordsByUsers(ctx, 100, start, end, []string{userIDs[i]}, nil)
		if err != nil {
			d.reqLog("checkin records %s (sdk): %v", userIDs[i], err)
			return
		}
		var tagged []interface{}
		for _, r := range recs {
			r["userid"] = userIDs[i]
			tagged = append(tagged, r)
		}
		res[i] = tagged
	})
	var all []interface{}
	for _, rs := range res {
		all = append(all, rs...)
	}
	return all, nil
}

// attendanceProbeResults is used by the permission probe to cover SDK surfaces.
func (d *Dumper) attendanceProbeResults(ctx context.Context) []map[string]interface{} {
	out := []map[string]interface{}{}
	item := map[string]interface{}{"api": "考勤-考勤组(SDK)"}
	if groups, _, err := d.AttendanceSimpleGroups(ctx, 10); err != nil {
		item["ok"] = false
		item["reason"] = truncate(err.Error(), 160)
		if m := deniedScopeRe.FindStringSubmatch(err.Error()); len(m) > 1 {
			item["missing_scope"] = m[1]
		}
	} else {
		item["ok"] = true
		item["count"] = len(groups)
	}
	out = append(out, item)

	item2 := map[string]interface{}{"api": "考勤-打卡记录(SDK)"}
	if recs, _, err := d.CheckinRecordsByUsers(ctx, 10, time.Now().AddDate(0, 0, -3).UnixMilli(), time.Now().UnixMilli(), nil, nil); err != nil {
		item2["ok"] = false
		item2["reason"] = truncate(err.Error(), 160)
		if m := deniedScopeRe.FindStringSubmatch(err.Error()); len(m) > 1 {
			item2["missing_scope"] = m[1]
		}
	} else {
		item2["ok"] = true
		item2["count"] = len(recs)
	}
	out = append(out, item2)
	return out
}
