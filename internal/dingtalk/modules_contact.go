package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"

	"sync"
)

func contactTasks() []task {
	g := "contact"
	return []task{
		{Group: g, Key: "departments", Desc: "部门树", Run: modDepartments},
		{Group: g, Key: "users", Desc: "用户列表", Run: modUsers},
		{Group: g, Key: "user_details", Desc: "用户详情(逐用户)", Run: modUserDetails},
	}
}

// deptChildren walks the department tree from a root id (BFS via listsub).
func (d *Dumper) deptChildren(ctx context.Context, deptID int64) ([]interface{}, error) {
	return d.CollectPages(ctx, PageSpec{
		Name: "contact_departments", Path: "/topapi/v2/department/listsub",
		Body: func(cursor int64, size int) map[string]interface{} {
			return map[string]interface{}{"dept_id": deptID}
		},
		Size: 100,
	})
}

// walkDeptTree returns every department (flat) via BFS from dept 1.
func (d *Dumper) walkDeptTree(ctx context.Context) ([]interface{}, error) {
	var all []interface{}
	seen := map[int64]bool{}
	queue := []int64{1}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		items, err := d.deptChildren(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("dept %d: %w", id, err)
		}
		for _, it := range items {
			m, _ := it.(map[string]interface{})
			all = append(all, m)
			if did := int64(numVal(m["dept_id"])); did > 0 && !seen[did] {
				queue = append(queue, did)
			}
		}
		if len(all) > 50000 {
			return nil, fmt.Errorf("dept tree safety limit reached")
		}
	}
	return all, nil
}

func modDepartments(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("contact.departments", func() (interface{}, error) {
		return d.walkDeptTree(ctx)
	})
}

// deptIDs returns reachable department ids; falls back to scoped departments
// when the tree walk is not permitted.
func (d *Dumper) deptIDs(ctx context.Context) ([]int64, error) {
	items, err := d.walkDeptTree(ctx)
	if err != nil {
		d.log("contact", "department walk unavailable: "+err.Error())
		return d.scopedDepts(ctx), nil
	}
	out := []int64{1}
	for _, it := range items {
		m, _ := it.(map[string]interface{})
		if did := int64(numVal(m["dept_id"])); did > 0 {
			out = append(out, did)
		}
	}
	return out, nil
}

// usersOfDept pages through /topapi/v2/user/list for one department.
func (d *Dumper) usersOfDept(ctx context.Context, deptID int64) ([]interface{}, error) {
	return d.CollectPages(ctx, PageSpec{
		Name: "contact_users", Path: "/topapi/v2/user/list",
		Body: func(cursor int64, size int) map[string]interface{} {
			return map[string]interface{}{"dept_id": deptID}
		},
		Size:     100,
		MaxItems: d.opt.MaxItems,
	})
}

func modUsers(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("contact.users", func() (interface{}, error) {
		ids, err := d.deptIDs(ctx)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var users []interface{}
		var mu sync.Mutex
		ids = append([]int64{1}, ids...) // 1 = root 无部门用户
		d.ForEach(ctx, d.opt.Workers, len(ids), func(i int) {
			items, err := d.usersOfDept(ctx, ids[i])
			if err != nil {
				d.reqLog("users dept %d: %v", ids[i], err)
				return
			}
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
		})
		// 通讯录授权范围兜底：authed_user 逐个查详情补齐列表
		for _, uid := range d.scopedUserIDs(ctx) {
			if !seen[uid] {
				seen[uid] = true
				users = append(users, map[string]interface{}{"userid": uid})
			}
		}
		return users, nil
	})
}

// userDetail fetches one user's full profile.
func (d *Dumper) userDetail(ctx context.Context, userid string) (map[string]interface{}, error) {
	data, err := d.do(ctx, callSpec{method: "POST", path: "/topapi/v2/user/get", body: map[string]interface{}{"userid": userid}})
	if err != nil {
		return nil, err
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if r, ok := root["result"].(map[string]interface{}); ok {
		return r, nil
	}
	return root, nil
}

func modUserDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	usersRaw, err := modUsers(ctx, d)
	if err != nil {
		return nil, err
	}
	users, _ := usersRaw.([]interface{})
	uids := make([]string, len(users))
	for i, u := range users {
		m, _ := u.(map[string]interface{})
		uids[i] = strVal(m["userid"])
	}
	res := make([]interface{}, len(uids))
	d.ForEach(ctx, d.opt.Workers, len(uids), func(i int) {
		if ctx.Err() != nil || uids[i] == "" {
			return
		}
		detail, err := d.userDetail(ctx, uids[i])
		if err != nil {
			d.reqLog("user %s detail: %v", uids[i], err)
			return
		}
		res[i] = detail
		// avatar 资源入队：来源 contact，id = userid
		if !d.opt.NoDownload {
			if av := strVal(detail["avatar"]); av != "" {
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
