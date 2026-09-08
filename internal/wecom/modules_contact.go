package wecom

import (
	"context"
	"fmt"
	"sync"
)

func contactTasks() []task {
	g := "contact"
	return []task{
		{Group: g, Key: "departments", Desc: "部门树", Run: modDepartments},
		{Group: g, Key: "users", Desc: "成员列表", Run: modUsers},
		{Group: g, Key: "user_details", Desc: "成员详情(逐成员)", Run: modUserDetails},
		{Group: g, Key: "tags", Desc: "标签及标签成员", Run: modTags},
	}
}

// modDepartments: /department/list 返回全量部门（含层级）；无权限时退
// /department/simplelist（仅 id/parentid）。
func modDepartments(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("contact.departments", func() (interface{}, error) {
		root, err := d.getJSON(ctx, "/department/list", nil)
		if err != nil {
			d.reqLog("department/list: %v (falling back to simplelist)", err)
			root, err = d.getJSON(ctx, "/department/simplelist", nil)
			if err != nil {
				return nil, err
			}
			return pickAny(root, "department_id"), nil
		}
		return pickAny(root, "department"), nil
	})
}

// deptIDs returns reachable department ids; root(1) always included as a
// starting point even when the tree is unavailable.
func (d *Dumper) deptIDs(ctx context.Context) []int64 {
	out := []int64{1}
	seen := map[int64]bool{1: true}
	raw, err := modDepartments(ctx, d)
	if err != nil {
		d.log("contact", "department list unavailable: "+err.Error())
		return out
	}
	items, _ := raw.([]interface{})
	for _, it := range items {
		m, _ := it.(map[string]interface{})
		if m == nil {
			continue
		}
		id := int64(numVal(m["id"]))
		if id == 0 {
			id = int64(numVal(m["department_id"])) // simplelist 形态
		}
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// usersOfDept: GET /user/list?department_id=X&fetch_child=0 —— 无分页，
// 返回该部门直属成员。
func (d *Dumper) usersOfDept(ctx context.Context, deptID int64, fetchChild int) ([]interface{}, error) {
	root, err := d.getJSON(ctx, "/user/list", map[string]string{
		"department_id": fmt.Sprintf("%d", deptID),
		"fetch_child":   fmt.Sprintf("%d", fetchChild),
	})
	if err != nil {
		return nil, err
	}
	list, _ := root["userlist"].([]interface{})
	return list, nil
}

func modUsers(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("contact.users", func() (interface{}, error) {
		seen := map[string]bool{}
		var users []interface{}
		var mu sync.Mutex
		appendUsers := func(items []interface{}) {
			mu.Lock()
			defer mu.Unlock()
			for _, it := range items {
				m, _ := it.(map[string]interface{})
				uid := strVal(m["userid"])
				if uid == "" || seen[uid] {
					continue
				}
				seen[uid] = true
				users = append(users, it)
				if !d.opt.NoDownload {
					if av := strVal(m["avatar"]); av != "" {
						d.AddResource("contact", uid, "", map[string]interface{}{"url": av, "name": "avatar"})
					}
				}
			}
		}

		ids := d.deptIDs(ctx)
		// 根部门可达时 fetch_child=1 一把抓全量，其余部门按直属遍历补齐
		fetchChild := 0
		if len(ids) == 1 {
			fetchChild = 1
		}
		d.ForEach(ctx, d.opt.Workers, len(ids), func(i int) {
			items, err := d.usersOfDept(ctx, ids[i], fetchChild)
			if err != nil {
				d.reqLog("users dept %d: %v", ids[i], err)
				return
			}
			appendUsers(items)
		})

		// 通讯录授权范围兜底：user/listid 返回全企业 userid（仅通讯录 secret）
		if len(users) == 0 {
			list, err := d.CollectPages(ctx, PageSpec{
				Name: "contact_userids", Path: "/user/listid",
				Body: func(cursor string, size int) map[string]interface{} {
					return map[string]interface{}{"cursor": cursor, "limit": size}
				},
				ListPath: "userid_list", NextPath: "next_cursor", Size: 10000,
			})
			if err != nil {
				d.reqLog("user/listid: %v", err)
			} else {
				for _, it := range list {
					uid := strVal(it)
					if uid != "" && !seen[uid] {
						seen[uid] = true
						users = append(users, map[string]interface{}{"userid": uid})
					}
				}
			}
		}
		return users, nil
	})
}

// userIDSet collects every userid seen so far (users + scoped fallbacks).
func (d *Dumper) userIDs(ctx context.Context) []string {
	raw, err := modUsers(ctx, d)
	if err != nil {
		return nil
	}
	list, _ := raw.([]interface{})
	out := make([]string, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]interface{})
		if uid := strVal(m["userid"]); uid != "" {
			out = append(out, uid)
		}
	}
	return out
}

func modUserDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	uids := d.userIDs(ctx)
	if len(uids) == 0 {
		return nil, fmt.Errorf("no userids available")
	}
	res := make([]interface{}, len(uids))
	d.ForEach(ctx, d.opt.Workers, len(uids), func(i int) {
		if ctx.Err() != nil {
			return
		}
		root, err := d.getJSON(ctx, "/user/get", map[string]string{"userid": uids[i]})
		if err != nil {
			d.reqLog("user %s get: %v", uids[i], err)
			return
		}
		delete(root, "errcode")
		delete(root, "errmsg")
		res[i] = root
		if !d.opt.NoDownload {
			if av := strVal(root["avatar"]); av != "" {
				d.AddResource("contact", uids[i], "", map[string]interface{}{"url": av, "name": "avatar"})
			}
		}
	})
	out := map[string]interface{}{}
	for i := range uids {
		if res[i] != nil {
			out[uids[i]] = res[i]
		}
	}
	return out, nil
}

// modTags: /tag/list + 逐标签 /tag/get（标签内成员/部门）。
func modTags(ctx context.Context, d *Dumper) (interface{}, error) {
	root, err := d.getJSON(ctx, "/tag/list", nil)
	if err != nil {
		return nil, err
	}
	tags, _ := root["taglist"].([]interface{})
	if len(tags) == 0 {
		return tags, nil
	}
	res := make([]interface{}, len(tags))
	d.ForEach(ctx, d.opt.Workers, len(tags), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, _ := tags[i].(map[string]interface{})
		tagid := fmt.Sprintf("%v", int64(numVal(m["tagid"])))
		detail, err := d.getJSON(ctx, "/tag/get", map[string]string{"tagid": tagid})
		if err != nil {
			d.reqLog("tag %s get: %v", tagid, err)
			res[i] = m
			return
		}
		delete(detail, "errcode")
		delete(detail, "errmsg")
		res[i] = detail
	})
	out := make([]interface{}, 0, len(tags))
	for _, r := range res {
		if r != nil {
			out = append(out, r)
		}
	}
	return out, nil
}
