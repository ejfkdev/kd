package feishu

import (
	"context"
	"fmt"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func calendarTasks() []task {
	g := "calendar"
	return []task{
		{Group: g, Key: "calendars", Desc: "日历列表", Run: modCalCalendars},
		{Group: g, Key: "primary", Desc: "主日历", Run: modCalPrimary},
		{Group: g, Key: "details", Desc: "日历信息(逐日历)", Run: modCalDetails},
		{Group: g, Key: "events", Desc: "日程列表(逐日历)", Run: modCalEvents},
		{Group: g, Key: "event_details", Desc: "日程详情(逐日程)", Run: modCalEventDetails},
		{Group: g, Key: "event_attendees", Desc: "日程参与人", Run: modCalAttendees},
		{Group: g, Key: "acls", Desc: "日历访问控制", Run: modCalAcls},
	}
}

func calendarWindow(d *Dumper) (int64, int64) {
	parse := func(s string) int64 {
		layouts := []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}
		for _, l := range layouts {
			if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
				return t.Unix()
			}
		}
		return 0
	}
	st := parse(d.opt.CalFrom)
	et := parse(d.opt.CalTo)
	if et == 0 {
		et = time.Now().AddDate(1, 0, 0).Unix()
	}
	if st == 0 {
		st = time.Now().AddDate(-1, 0, 0).Unix()
	}
	return st, et
}

func modCalCalendars(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("calendar.calendars", func() (interface{}, error) {
		return d.Collect(ctx, ListConf{
			Name: "cal_calendars", Method: "GET", Path: "/open-apis/calendar/v4/calendars",
			Supports: SupUser | SupTenant, PageSize: 50, ListKey: "calendar_list",
		})
	})
}

func modCalPrimary(ctx context.Context, d *Dumper) (interface{}, error) {
	m, err := d.CollectRoot(ctx, "cal_primary", "POST", "/open-apis/calendar/v4/calendars/primary", nil, nil, map[string]interface{}{}, SupUser|SupTenant)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func calendarIDs(ctx context.Context, d *Dumper) ([]string, error) {
	cals, err := modCalCalendars(ctx, d)
	if err != nil {
		return nil, err
	}
	arr, _ := cals.([]interface{})
	var ids []string
	seen := map[string]bool{}
	for _, c := range arr {
		m, _ := c.(map[string]interface{})
		var cid string
		if cal, ok := m["calendar"].(map[string]interface{}); ok {
			cid = strVal(cal["calendar_id"])
		}
		if cid == "" {
			cid = strVal(m["calendar_id"])
		}
		if cid != "" && !seen[cid] {
			seen[cid] = true
			ids = append(ids, cid)
		}
	}
	return ids, nil
}

func modCalDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	ids, err := calendarIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	for _, cid := range ids {
		if ctx.Err() != nil {
			break
		}
		m, err := d.CollectObj(ctx, "cal_details", "GET", "/open-apis/calendar/v4/calendars/"+cid, nil, nil, SupUser|SupTenant)
		if err != nil {
			d.reqLog("calendar %s: %v", cid, err)
			continue
		}
		out[cid] = m
	}
	return out, nil
}

func modCalEvents(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("calendar.events", func() (interface{}, error) {
		ids, err := calendarIDs(ctx, d)
		if err != nil {
			return nil, err
		}
		st, et := calendarWindow(d)
		all := []interface{}{}
		for _, cid := range ids {
			if ctx.Err() != nil {
				break
			}
			q := larkcore.QueryParams{}
			q.Set("start_time", fmt.Sprintf("%d", st))
			q.Set("end_time", fmt.Sprintf("%d", et))
			items, err := d.Collect(ctx, ListConf{
				Name: "cal_events", Method: "GET", Path: "/open-apis/calendar/v4/calendars/" + cid + "/events",
				Query: q, Supports: SupUser | SupTenant, PageSize: 500,
			})
			if err != nil {
				all = append(all, map[string]interface{}{"_error": err.Error(), "calendar_id": cid})
				continue
			}
			all = append(all, attach(items, "calendar_id", cid)...)
			d.logf("cal_events", "%s: %d events", cid, len(items))
		}
		return all, nil
	})
}

func modCalEventDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	events, err := modCalEvents(ctx, d)
	if err != nil {
		return nil, err
	}
	arr, _ := events.([]interface{})
	out := make([]interface{}, len(arr))
	d.ForEach(ctx, d.opt.Workers, len(arr), func(i int) {
		if ctx.Err() != nil {
			return
		}
		ev, ok := arr[i].(map[string]interface{})
		if !ok {
			return
		}
		if _, isErr := ev["_error"]; isErr {
			return
		}
		eid := strVal(ev["event_id"])
		cid := strVal(ev["calendar_id"])
		if eid == "" || cid == "" {
			return
		}
		detail, err := d.CollectObj(ctx, "cal_event_details", "GET", "/open-apis/calendar/v4/calendars/"+cid+"/events/"+eid, nil, nil, SupUser|SupTenant)
		if err != nil {
			d.reqLog("event %s: %v", eid, err)
			return
		}
		evm, _ := detail["event"].(map[string]interface{})
		out[i] = evm
		if evm != nil && !d.opt.NoDownload {
			if atts, ok := evm["attachments"].([]interface{}); ok {
				for _, a := range atts {
					am, _ := a.(map[string]interface{})
					if boolVal(am["is_deleted"]) {
						continue
					}
					d.AddResource("calendar", strVal(am["file_token"]), strVal(am["name"]), map[string]interface{}{
						"file_token": strVal(am["file_token"]), "event_id": eid, "calendar_id": cid,
					})
				}
			}
		}
	})
	res := map[string]interface{}{}
	for i := range arr {
		if v := out[i]; v != nil {
			ev, _ := arr[i].(map[string]interface{})
			res[strVal(ev["event_id"])] = v
		}
	}
	return res, nil
}

func modCalAttendees(ctx context.Context, d *Dumper) (interface{}, error) {
	events, err := modCalEvents(ctx, d)
	if err != nil {
		return nil, err
	}
	arr, _ := events.([]interface{})
	results := make([][]interface{}, len(arr))
	d.ForEach(ctx, d.opt.Workers, len(arr), func(i int) {
		if ctx.Err() != nil {
			return
		}
		ev, ok := arr[i].(map[string]interface{})
		if !ok {
			return
		}
		eid := strVal(ev["event_id"])
		cid := strVal(ev["calendar_id"])
		if eid == "" || cid == "" {
			return
		}
		q := uidTypeQ(d)
		items, err := d.Collect(ctx, ListConf{
			Name: "cal_event_attendees", Method: "GET",
			Path:     "/open-apis/calendar/v4/calendars/" + cid + "/events/" + eid + "/attendees",
			Query:    q,
			Supports: SupUser | SupTenant, PageSize: 100,
		})
		if err != nil {
			d.reqLog("event %s attendees: %v", eid, err)
			return
		}
		var tagged []interface{}
		for _, it := range items {
			im, _ := it.(map[string]interface{})
			nm := map[string]interface{}{"event_id": eid, "calendar_id": cid}
			for k, v := range im {
				nm[k] = v
			}
			tagged = append(tagged, nm)
		}
		results[i] = tagged
	})
	var all []interface{}
	for _, rs := range results {
		all = append(all, rs...)
	}
	return all, nil
}

func modCalAcls(ctx context.Context, d *Dumper) (interface{}, error) {
	ids, err := calendarIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	all := map[string]interface{}{}
	for _, cid := range ids {
		if ctx.Err() != nil {
			break
		}
		q := uidTypeQ(d)
		items, err := d.Collect(ctx, ListConf{
			Name: "cal_acls", Method: "GET", Path: "/open-apis/calendar/v4/calendars/" + cid + "/acls",
			Query: q, Supports: SupUser | SupTenant, PageSize: 100, ListKey: "acls",
		})
		if err != nil {
			d.reqLog("calendar %s acls: %v", cid, err)
			continue
		}
		all[cid] = items
	}
	return all, nil
}
