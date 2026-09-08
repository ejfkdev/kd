package wecom

import (
	"context"
	"fmt"
	"sync"
	"time"
)

func externalTasks() []task {
	g := "external"
	return []task{
		{Group: g, Key: "follow_users", Desc: "客户联系功能成员", Run: modFollowUsers},
		{Group: g, Key: "corp_tags", Desc: "企业客户标签库", Run: modCorpTags},
		{Group: g, Key: "contact_ways", Desc: "联系我渠道列表", Run: modContactWays},
		{Group: g, Key: "customers", Desc: "客户列表(逐成员)", Run: modCustomers},
		{Group: g, Key: "customer_details", Desc: "客户详情", Run: modCustomerDetails},
		{Group: g, Key: "group_chats", Desc: "客户群列表", Run: modGroupChats},
		{Group: g, Key: "group_chat_details", Desc: "客户群详情", Run: modGroupChatDetails},
		{Group: g, Key: "unassigned", Desc: "离职待继承客户", Run: modUnassigned},
		{Group: g, Key: "moments", Desc: "朋友圈(近一年)", Run: modMoments},
	}
}

// modFollowUsers: 配置了客户联系功能的成员列表。
func modFollowUsers(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("external.follow_users", func() (interface{}, error) {
		root, err := d.getJSON(ctx, "/externalcontact/get_follow_user_list", nil)
		if err != nil {
			return nil, err
		}
		return pickAny(root, "follow_user"), nil
	})
}

func modCorpTags(ctx context.Context, d *Dumper) (interface{}, error) {
	root, err := d.postJSON(ctx, "/externalcontact/corp_tag/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	return pickAny(root, "tag_group"), nil
}

func modContactWays(ctx context.Context, d *Dumper) (interface{}, error) {
	root, err := d.postJSON(ctx, "/externalcontact/contact_way/list", map[string]interface{}{"limit": 1000})
	if err != nil {
		return nil, err
	}
	return pickAny(root, "contact_way"), nil
}

// customerUserIDs 决定按哪些成员枚举客户：优先客户联系功能成员，退全员。
func (d *Dumper) customerUserIDs(ctx context.Context) []string {
	raw, err := modFollowUsers(ctx, d)
	if err == nil {
		if list, ok := raw.([]interface{}); ok && len(list) > 0 {
			out := make([]string, 0, len(list))
			for _, it := range list {
				if s := strVal(it); s != "" {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return d.userIDs(ctx)
}

func modCustomers(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("external.customers", func() (interface{}, error) {
		uids := d.customerUserIDs(ctx)
		if len(uids) == 0 {
			return nil, fmt.Errorf("no member userids available for externalcontact/list")
		}
		type extItem struct {
			ExternalUserID string
			OwnerUserIDs   []string
		}
		var mu sync.Mutex
		seen := map[string]bool{}
		var out []interface{}
		d.ForEach(ctx, d.opt.Workers, len(uids), func(i int) {
			if ctx.Err() != nil {
				return
			}
			root, err := d.getJSON(ctx, "/externalcontact/list", map[string]string{"userid": uids[i]})
			if err != nil {
				d.reqLog("externalcontact/list %s: %v", uids[i], err)
				return
			}
			list, _ := root["external_userid"].([]interface{})
			mu.Lock()
			defer mu.Unlock()
			for _, it := range list {
				euid := strVal(it)
				if euid == "" {
					continue
				}
				if !seen[euid] {
					seen[euid] = true
					out = append(out, map[string]interface{}{
						"external_userid": euid,
						"follow_users":    []string{uids[i]},
					})
					continue
				}
				// 已在列表中：把当前成员追加到 follow_users
				for _, rec := range out {
					m, _ := rec.(map[string]interface{})
					if strVal(m["external_userid"]) == euid {
						fu, _ := m["follow_users"].([]string)
						m["follow_users"] = append(fu, uids[i])
						break
					}
				}
			}
		})
		return out, nil
	})
}

func modCustomerDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	raw, err := modCustomers(ctx, d)
	if err != nil {
		return nil, err
	}
	list, _ := raw.([]interface{})
	ids := make([]string, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]interface{})
		if s := strVal(m["external_userid"]); s != "" {
			ids = append(ids, s)
		}
	}
	res := make([]interface{}, len(ids))
	d.ForEach(ctx, d.opt.Workers, len(ids), func(i int) {
		if ctx.Err() != nil {
			return
		}
		root, err := d.getJSON(ctx, "/externalcontact/get", map[string]string{"external_userid": ids[i]})
		if err != nil {
			d.reqLog("externalcontact/get %s: %v", ids[i], err)
			return
		}
		delete(root, "errcode")
		delete(root, "errmsg")
		res[i] = root
		if !d.opt.NoDownload {
			if ec, ok := root["external_contact"].(map[string]interface{}); ok {
				if av := strVal(ec["avatar"]); av != "" {
					d.AddResource("external", ids[i], "", map[string]interface{}{"url": av, "name": "avatar"})
				}
			}
		}
	})
	out := map[string]interface{}{}
	for i := range ids {
		if res[i] != nil {
			out[ids[i]] = res[i]
		}
	}
	return out, nil
}

func modGroupChats(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("external.group_chats", func() (interface{}, error) {
		return d.CollectPages(ctx, PageSpec{
			Name: "external_group_chats", Path: "/externalcontact/groupchat/list",
			Body: func(cursor string, size int) map[string]interface{} {
				return map[string]interface{}{"status_filter": 0, "limit": size, "cursor": cursor}
			},
			ListPath: "group_chat_list", NextPath: "next_cursor", MorePath: "has_more", MoreIs: true,
			Size: 100,
		})
	})
}

func modGroupChatDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	raw, err := modGroupChats(ctx, d)
	if err != nil {
		return nil, err
	}
	list, _ := raw.([]interface{})
	ids := make([]string, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]interface{})
		if s := strVal(m["chat_id"]); s != "" {
			ids = append(ids, s)
		}
	}
	res := make([]interface{}, len(ids))
	d.ForEach(ctx, d.opt.Workers, len(ids), func(i int) {
		if ctx.Err() != nil {
			return
		}
		root, err := d.postJSON(ctx, "/externalcontact/groupchat/get", map[string]interface{}{"chat_id": ids[i], "need_name": 1})
		if err != nil {
			d.reqLog("groupchat/get %s: %v", ids[i], err)
			return
		}
		res[i] = root["group_chat"]
	})
	out := map[string]interface{}{}
	for i := range ids {
		if res[i] != nil {
			out[ids[i]] = res[i]
		}
	}
	return out, nil
}

func modUnassigned(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.CollectPages(ctx, PageSpec{
		Name: "external_unassigned", Path: "/externalcontact/get_unassigned_list",
		Body: func(cursor string, size int) map[string]interface{} {
			return map[string]interface{}{"page_id": cursor, "page_size": size}
		},
		ListPath: "info_list", NextPath: "next_page_id", MorePath: "is_last", MoreIs: false,
		Size: 1000,
	})
}

// modMoments 按月窗口拉近一年朋友圈（接口限制单次查询时间跨度）。
func modMoments(ctx context.Context, d *Dumper) (interface{}, error) {
	var all []interface{}
	end := time.Now()
	for w := 0; w < 12; w++ {
		if ctx.Err() != nil {
			break
		}
		start := end.AddDate(0, 0, -29)
		wEnd, wStart := end.Unix(), start.Unix()
		items, err := d.CollectPages(ctx, PageSpec{
			Name: "external_moments", Path: "/externalcontact/moment_list",
			Body: func(cursor string, size int) map[string]interface{} {
				return map[string]interface{}{
					"start_time": wStart, "end_time": wEnd,
					"filter": map[string]interface{}{"creator_userid_list": []string{}},
					"limit":  size, "cursor": cursor,
				}
			},
			ListPath: "moment_list", NextPath: "next_cursor", MorePath: "has_more", MoreIs: true,
			Size: 100, MaxItems: d.opt.MaxItems,
		})
		if err != nil {
			d.reqLog("moment_list window %d: %v", w, err)
			// 首个窗口即失败视为无朋友圈权限，直接放弃
			if w == 0 {
				return nil, err
			}
		}
		all = append(all, items...)
		end = start.Add(-24 * time.Hour)
	}
	return all, nil
}
