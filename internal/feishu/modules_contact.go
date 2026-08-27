package feishu

import (
	"context"
	"fmt"
	"strings"
	"sync"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func contactTasks() []task {
	g := "contact"
	return []task{
		{Group: g, Key: "scopes", Desc: "通讯录授权范围", Run: modContactScopes},
		{Group: g, Key: "departments", Desc: "部门树", Run: modContactDepartments},
		{Group: g, Key: "users", Desc: "用户列表", Run: modContactUsers},
		{Group: g, Key: "user_details", Desc: "用户详情(逐用户)", Run: modContactUserDetails},
		{Group: g, Key: "groups", Desc: "用户组", Run: modContactGroups},
		{Group: g, Key: "group_members", Desc: "用户组成员", Run: modContactGroupMembers},
		{Group: g, Key: "custom_attrs", Desc: "自定义用户字段", Run: modContactCustomAttrs},
		{Group: g, Key: "employee_types", Desc: "人员类型", Run: modContactEmployeeTypes},
		{Group: g, Key: "job_family", Desc: "职务序列", Run: modContactJobFamily},
		{Group: g, Key: "job_level", Desc: "职务级别", Run: modContactJobLevel},
		{Group: g, Key: "work_cities", Desc: "工作城市", Run: modContactWorkCity},
		{Group: g, Key: "units", Desc: "单位", Run: modContactUnits},
	}
}

func uidTypeQ(d *Dumper) larkcore.QueryParams {
	q := larkcore.QueryParams{}
	q.Set("user_id_type", d.opt.UserIDType)
	return q
}

// modContactScopes: the departments/user_ids/group_ids the app may access.
func modContactScopes(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("contact.scopes", func() (interface{}, error) {
		out := map[string]interface{}{}
		pageToken := ""
		for page := 1; ; page++ {
			q := larkcore.QueryParams{}
			q.Set("page_size", "50")
			q.Set("user_id_type", d.opt.UserIDType)
			if pageToken != "" {
				q.Set("page_token", pageToken)
			}
			resp, err := d.do(ctx, CallSpec{Method: "GET", Path: "/open-apis/contact/v3/scopes", Query: q, Supports: SupTenant})
			if err != nil {
				if page == 1 {
					return nil, err
				}
				break
			}
			root, err := decodeMap(resp.RawBody)
			if err != nil {
				return nil, err
			}
			data := subMap(root, "data")
			appendList := func(key string) {
				if arr, ok := data[key].([]interface{}); ok {
					if cur, ok := out[key].([]interface{}); ok {
						out[key] = append(cur, arr...)
					} else {
						out[key] = arr
					}
				}
			}
			appendList("department_ids")
			appendList("user_ids")
			appendList("group_ids")
			if !boolVal(data["has_more"]) {
				break
			}
			pageToken = strVal(data["page_token"])
			if pageToken == "" {
				break
			}
		}
		return out, nil
	})
}

// batchUsersByIds fetches users by open_id via /contact/v3/users/batch. A chunk
// that contains out-of-scope ids fails whole (99992361 open_id cross app), so
// failed chunks degrade to per-id requests keeping the ids the app can read.
func batchUsersByIds(ctx context.Context, d *Dumper, ids []string) ([]interface{}, error) {
	var users []interface{}
	fetchChunk := func(chunk []string) ([]interface{}, error) {
		q := uidTypeQ(d)
		for _, id := range chunk {
			q.Add("user_ids", id)
		}
		return d.Collect(ctx, ListConf{
			Name: "contact_users_batch", Method: "GET", Path: "/open-apis/contact/v3/users/batch",
			Query: q, Supports: SupTenant | SupUser, PageSize: 50,
		})
	}
	for i := 0; i < len(ids); i += 50 {
		end := i + 50
		if end > len(ids) {
			end = len(ids)
		}
		items, err := fetchChunk(ids[i:end])
		if err == nil {
			users = append(users, items...)
			continue
		}
		d.log("contact_users", fmt.Sprintf("batch %d failed (%v), retrying per id", len(ids[i:end]), err))
		for _, id := range ids[i:end] {
			one, err := fetchChunk([]string{id})
			if err != nil {
				d.reqLog("contact user %s: %v", id, err)
				continue
			}
			users = append(users, one...)
		}
	}
	return users, nil
}

// scopedAndPooledUsers lists users from 通讯录授权范围 plus ids harvested from
// readable data (message senders/mentions) when department enumeration fails.
func scopedAndPooledUsers(ctx context.Context, d *Dumper) (interface{}, error) {
	var ids []string
	seen := map[string]bool{}
	scopesRaw, err := d.memoized("contact.scopes", func() (interface{}, error) { return modContactScopes(ctx, d) })
	if err == nil {
		scopes, _ := scopesRaw.(map[string]interface{})
		idsRaw, _ := scopes["user_ids"].([]interface{})
		for _, id := range idsRaw {
			if v := strVal(id); v != "" && !seen[v] {
				seen[v] = true
				ids = append(ids, v)
			}
		}
	}
	// harvested ids from messages/mentions (open_id only; batch API needs open_id)
	for _, id := range d.ids("user") {
		if strings.HasPrefix(id, "ou_") && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return []interface{}{}, nil
	}
	return batchUsersByIds(ctx, d, ids)
}

// walkDepartments returns every department id (children walk from root).
func walkDepartments(ctx context.Context, d *Dumper) ([]string, error) {
	var seen = map[string]bool{}
	var order []string
	var queue = []string{"0"}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		items, err := d.Collect(ctx, ListConf{
			Name:     "dept",
			Method:   "GET",
			Path:     "/open-apis/contact/v3/departments/" + id + "/children",
			Query:    uidTypeQ(d),
			Supports: SupTenant | SupUser,
			ListKey:  "items",
			PageSize: 50,
		})
		if err != nil {
			return nil, fmt.Errorf("departments(%s): %w", id, err)
		}
		for _, it := range items {
			m, _ := it.(map[string]interface{})
			name := strVal(m["name"])
			did := strVal(m["department_id"])
			if did == "" {
				did = strVal(m["open_department_id"])
			}
			did = strings.TrimSpace(did)
			if did == "" {
				continue
			}
			if seen[did] {
				continue
			}
			seen[did] = true
			order = append(order, did)
			queue = append(queue, did)
			d.logf("dept", "walk: %s", name)
		}
		if len(order) > 100000 {
			return nil, fmt.Errorf("department walk safety limit reached")
		}
	}
	return order, nil
}

func modContactDepartments(ctx context.Context, d *Dumper) (interface{}, error) {
	var depts []interface{}
	seen := map[string]bool{}
	var walk func(id string) error
	walk = func(id string) error {
		items, err := d.Collect(ctx, ListConf{
			Name: "contact_departments", Method: "GET",
			Path:     "/open-apis/contact/v3/departments/" + id + "/children",
			Query:    uidTypeQ(d),
			Supports: SupTenant | SupUser,
			PageSize: 50,
		})
		if err != nil {
			return err
		}
		children := []string{}
		for _, it := range items {
			m, _ := it.(map[string]interface{})
			did := strVal(m["department_id"])
			if did == "" {
				did = strVal(m["open_department_id"])
			}
			if did == "" || did == id {
				continue
			}
			if !seen[did] {
				seen[did] = true
				depts = append(depts, it)
				children = append(children, did)
			}
		}
		for _, c := range children {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("0"); err != nil {
		return nil, err
	}
	return depts, nil
}

func modContactUsers(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("contact.users", func() (interface{}, error) {
		// find all department ids first (from the departments module if possible, else self-walk)
		deptIDs, err := walkDepartments(ctx, d)
		if err != nil {
			d.log("contact_users", "departments unavailable, falling back to scoped+harvested user ids: "+err.Error())
			return scopedAndPooledUsers(ctx, d)
		}
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var users []interface{}
		for _, did := range deptIDs {
			items, err := d.Collect(ctx, ListConf{
				Name:   "contact_users",
				Method: "GET",
				Path:   "/open-apis/contact/v3/users",
				Query: func() larkcore.QueryParams {
					q := uidTypeQ(d)
					q.Set("department_id", did)
					return q
				}(),
				Supports: SupTenant | SupUser,
				PageSize: 50,
			})
			if err != nil {
				d.log("contact_users", fmt.Sprintf("dept %s: %v", did, err))
				continue
			}
			for _, it := range items {
				m, _ := it.(map[string]interface{})
				key := userKey(m)
				if key == "" || seen[key] {
					continue
				}
				seen[key] = true
				users = append(users, it)
			}
		}
		// also fetch root (department_id=0) users in case walk missed them
		rootUsers, err := d.Collect(ctx, ListConf{
			Name: "contact_users", Method: "GET", Path: "/open-apis/contact/v3/users",
			Query: func() larkcore.QueryParams {
				q := uidTypeQ(d)
				q.Set("department_id", "0")
				return q
			}(),
			Supports: SupTenant | SupUser, PageSize: 50,
		})
		if err == nil {
			for _, it := range rootUsers {
				m, _ := it.(map[string]interface{})
				key := userKey(m)
				if key != "" && !seen[key] {
					seen[key] = true
					users = append(users, it)
				}
			}
		} else {
			d.log("contact_users", "root users: "+err.Error())
		}
		// harvested ids from messages: supplement department-walk results
		var extra []string
		for _, id := range d.ids("user") {
			if strings.HasPrefix(id, "ou_") && !seen["open_id:"+id] {
				extra = append(extra, id)
			}
		}
		if len(extra) > 0 {
			if ux, err := batchUsersByIds(ctx, d, extra); err == nil {
				for _, it := range ux {
					m, _ := it.(map[string]interface{})
					key := userKey(m)
					if key == "" || seen[key] {
						continue
					}
					seen[key] = true
					users = append(users, it)
				}
			}
		}
		return users, nil
	})
}

func userKey(m map[string]interface{}) string {
	for _, k := range []string{"open_id", "user_id", "union_id"} {
		if v := strVal(m[k]); v != "" {
			return k + ":" + v
		}
	}
	return ""
}

func modContactUserDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	if !d.opt.UserDetails {
		return map[string]interface{}{"note": "disabled (-no-user-details)"}, nil
	}
	usersRaw, err := modContactUsers(ctx, d)
	if err != nil {
		return nil, err
	}
	users, _ := usersRaw.([]interface{})
	details := map[string]interface{}{}
	var mu sync.Mutex
	d.ForEach(ctx, d.opt.Workers, len(users), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, _ := users[i].(map[string]interface{})
		uid := strVal(m["open_id"])
		if uid == "" {
			uid = strVal(m["user_id"])
		}
		if uid == "" {
			return
		}
		m2, err := d.CollectObj(ctx, "contact_user_details", "GET", "/open-apis/contact/v3/users/"+uid, nil, uidTypeQ(d), SupTenant|SupUser)
		if err != nil {
			d.reqLog("contact user %s: %v", uid, err)
			return
		}
		mu.Lock()
		details[uid] = m2["user"]
		mu.Unlock()
	})
	return details, nil
}

func modContactGroups(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{
		Name: "contact_groups", Method: "GET",
		Path:     "/open-apis/contact/v3/group/simplelist",
		Supports: SupTenant, PageSize: 100, ListKey: "grouplist",
	})
}

func modContactGroupMembers(ctx context.Context, d *Dumper) (interface{}, error) {
	groupsRaw, err := modContactGroups(ctx, d)
	if err != nil {
		return nil, err
	}
	groups, _ := groupsRaw.([]interface{})
	res := map[string]interface{}{}
	for _, g := range groups {
		m, _ := g.(map[string]interface{})
		gid := strVal(m["group_id"])
		if gid == "" {
			continue
		}
		q := larkcore.QueryParams{}
		q.Set("member_id_type", d.opt.UserIDType)
		members, err := d.Collect(ctx, ListConf{
			Name: "contact_group_members", Method: "GET",
			Path:     "/open-apis/contact/v3/group/" + gid + "/member/simplelist",
			Query:    q,
			Supports: SupTenant, PageSize: 100, ListKey: "memberlist",
		})
		if err != nil {
			d.reqLog("group %s members: %v", gid, err)
			continue
		}
		res[gid] = members
	}
	return res, nil
}

func modContactCustomAttrs(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{Name: "contact_custom_attrs", Method: "GET", Path: "/open-apis/contact/v3/custom_attrs", Supports: SupTenant, PageSize: 100})
}

func modContactEmployeeTypes(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{Name: "contact_employee_types", Method: "GET", Path: "/open-apis/contact/v3/employee_type_enums", Supports: SupTenant, PageSize: 100})
}

func modContactJobFamily(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{Name: "contact_job_family", Method: "GET", Path: "/open-apis/contact/v3/job_families", Supports: SupTenant, PageSize: 50})
}

func modContactJobLevel(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{Name: "contact_job_level", Method: "GET", Path: "/open-apis/contact/v3/job_levels", Supports: SupTenant, PageSize: 50})
}

func modContactWorkCity(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{Name: "contact_work_cities", Method: "GET", Path: "/open-apis/contact/v3/work_cities", Supports: SupTenant, PageSize: 50})
}

func modContactUnits(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.Collect(ctx, ListConf{Name: "contact_units", Method: "GET", Path: "/open-apis/contact/v3/unit", Supports: SupTenant, PageSize: 100, ListKey: "unitlist"})
}

// attach copies each item map and adds key=val (used to tag items with parent ids).
func attach(items []interface{}, key string, val interface{}) []interface{} {
	out := make([]interface{}, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			out = append(out, it)
			continue
		}
		nm := make(map[string]interface{}, len(m)+1)
		for k, v := range m {
			nm[k] = v
		}
		nm[key] = val
		out = append(out, nm)
	}
	return out
}
