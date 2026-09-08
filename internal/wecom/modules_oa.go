package wecom

import (
	"context"
	"strconv"
	"sync"
)

func oaTasks() []task {
	g := "oa"
	return []task{
		{Group: g, Key: "meeting_rooms", Desc: "会议室列表", Run: modMeetingRooms},
		{Group: g, Key: "livings", Desc: "成员直播ID列表", Run: modLivings},
		{Group: g, Key: "living_details", Desc: "直播详情", Run: modLivingDetails},
	}
}

func modMeetingRooms(ctx context.Context, d *Dumper) (interface{}, error) {
	root, err := d.postJSON(ctx, "/oa/meetingroom/list", map[string]interface{}{"limit": 1000, "offset": 0})
	if err != nil {
		return nil, err
	}
	return pickAny(root, "meeting_room_list"), nil
}

// modLivings: 逐成员枚举直播 id（/living/get_user_all_livingid，游标分页）。
func modLivings(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("oa.livings", func() (interface{}, error) {
		uids := d.userIDs(ctx)
		if len(uids) == 0 {
			return []interface{}{}, nil
		}
		var mu sync.Mutex
		var all []interface{}
		d.ForEach(ctx, d.opt.Workers, len(uids), func(i int) {
			if ctx.Err() != nil {
				return
			}
			items, err := d.CollectPages(ctx, PageSpec{
				Name: "living_ids", Path: "/living/get_user_all_livingid",
				Body: func(cursor string, size int) map[string]interface{} {
					c, _ := strconv.Atoi(cursor)
					return map[string]interface{}{"userid": uids[i], "cursor": c, "limit": size}
				},
				ListPath: "livingid_list", NextPath: "next_cursor", Size: 100,
			})
			if err != nil {
				d.reqLog("get_user_all_livingid %s: %v", uids[i], err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, it := range items {
				if s := strVal(it); s != "" {
					all = append(all, map[string]interface{}{"livingid": s, "userid": uids[i]})
				}
			}
		})
		return all, nil
	})
}

func modLivingDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	raw, err := modLivings(ctx, d)
	if err != nil {
		return nil, err
	}
	list, _ := raw.([]interface{})
	if len(list) == 0 {
		return nil, nil
	}
	res := make([]interface{}, len(list))
	d.ForEach(ctx, d.opt.Workers, len(list), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, _ := list[i].(map[string]interface{})
		lid := strVal(m["livingid"])
		root, err := d.getJSON(ctx, "/living/get_living_info", map[string]string{"livingid": lid})
		if err != nil {
			d.reqLog("get_living_info %s: %v", lid, err)
			return
		}
		info, _ := root["living_info"].(map[string]interface{})
		if info == nil {
			return
		}
		info["userid"] = m["userid"]
		res[i] = info
	})
	out := make([]interface{}, 0, len(list))
	for _, r := range res {
		if r != nil {
			out = append(out, r)
		}
	}
	return out, nil
}
