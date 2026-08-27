package dingtalk

import (
	"context"
	"encoding/json"
	"regexp"
)

func miscTasks() []task {
	g := "misc"
	return []task{
		{Group: g, Key: "appinfo", Desc: "应用信息", Run: modAppInfo},
		{Group: g, Key: "permission_probe", Desc: "权限边界探测", Run: modPermissionProbe},
	}
}

// modAppInfo: 企业内部应用基本信息（一般无需额外权限）。
func modAppInfo(ctx context.Context, d *Dumper) (interface{}, error) {
	data, err := d.do(ctx, callSpec{method: "GET", path: "/appinfo/get"})
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

var deniedScopeRe = regexp.MustCompile(`权限：\[([a-zA-Z0-9_]+)`)

// probeDef names one boundary probe.
type probeDef struct {
	Name string
	Path string
	Body map[string]interface{}
}

// modPermissionProbe maps what the credential can reach across the major
// DingTalk API surfaces; every probe is logged either ok or with the denied
// scope parsed from the 88+60011 message.
func modPermissionProbe(ctx context.Context, d *Dumper) (interface{}, error) {
	probes := []probeDef{
		{"通讯录-部门列表", "/topapi/v2/department/listsub", map[string]interface{}{"dept_id": 1}},
		{"通讯录-部门用户列表", "/topapi/v2/user/list", map[string]interface{}{"dept_id": 1, "cursor": 0, "size": 1}},
		{"通讯录-用户详情", "/topapi/v2/user/get", map[string]interface{}{"userid": "manager"}},
		{"考勤-考勤组", "/topapi/attendance/group/list", map[string]interface{}{"offset": 0, "size": 1}},
		{"审批-实例列表", "/topapi/processinstance/listids", map[string]interface{}{"process_code": "", "start_time": 0, "end_time": 0, "size": 1, "cursor": 0}},
		{"人事-在职员工", "/topapi/smartwork/hrm/employee/queryonjob", map[string]interface{}{"status_list": "2,3,5", "offset": 0, "size": 1}},
		{"媒体-下载", "/media/download", map[string]interface{}{"media_id": "probe"}},
	}
	out := []interface{}{}
	for _, p := range probes {
		if ctx.Err() != nil {
			break
		}
		item := map[string]interface{}{"api": p.Name}
		data, err := d.do(ctx, callSpec{method: "POST", path: p.Path, body: p.Body})
		if err == nil {
			item["ok"] = true
			item["sample"] = truncate(string(data), 200)
		} else if de, ok := err.(*DumpError); ok {
			item["ok"] = false
			item["errcode"] = de.Code
			if m := deniedScopeRe.FindStringSubmatch(de.Msg); len(m) > 1 {
				item["missing_scope"] = m[1]
				item["reason"] = "missing permission"
			} else {
				item["reason"] = truncate(de.Msg, 160)
			}
		} else {
			item["ok"] = false
			item["reason"] = err.Error()
		}
		out = append(out, item)
	}
	// SDK 面探测
	for _, it := range d.attendanceProbeResults(ctx) {
		out = append(out, it)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
