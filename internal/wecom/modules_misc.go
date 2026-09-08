package wecom

import (
	"context"
	"strconv"
	"time"
)

func miscTasks() []task {
	g := "misc"
	return []task{
		{Group: g, Key: "permission_probe", Desc: "权限边界探测", Run: modPermissionProbe},
	}
}

// probeDef names one boundary probe.
type probeDef struct {
	Name   string
	Method string
	Path   string
	Query  map[string]string
	Body   map[string]interface{}
}

// modPermissionProbe maps what the credential can reach across the major
// WeCom API surfaces; every probe is logged ok or with the denial reason
// (60011 = 无权限, 48002 = api 禁止调用, 301048 = 应用无权限 …)。
func modPermissionProbe(ctx context.Context, d *Dumper) (interface{}, error) {
	now := strconv.FormatInt(time.Now().Unix(), 10)
	probes := []probeDef{
		{"通讯录-部门列表", "GET", "/department/list", nil, nil},
		{"通讯录-部门成员", "GET", "/user/list", map[string]string{"department_id": "1", "fetch_child": "1"}, nil},
		{"通讯录-成员ID列表", "POST", "/user/listid", nil, map[string]interface{}{"cursor": "", "limit": 1}},
		{"通讯录-标签列表", "GET", "/tag/list", nil, nil},
		{"客户联系-功能成员", "GET", "/externalcontact/get_follow_user_list", nil, nil},
		{"客户联系-企业标签", "POST", "/externalcontact/corp_tag/list", nil, map[string]interface{}{}},
		{"客户联系-客户群", "POST", "/externalcontact/groupchat/list", nil, map[string]interface{}{"status_filter": 0, "limit": 1}},
		{"审批-表单模板", "POST", "/oa/dialist", nil, map[string]interface{}{}},
		{"审批-单号列表", "POST", "/oa/getapprovalinfo", nil, map[string]interface{}{"starttime": now, "endtime": now, "cursor": 0, "size": 1, "filters": []interface{}{}}},
		{"打卡-打卡规则", "POST", "/checkin/getcheckinoption", nil, map[string]interface{}{"datetime": time.Now().UnixMilli(), "useridlist": []string{"probe"}}},
		{"会议-会议室列表", "POST", "/oa/meetingroom/list", nil, map[string]interface{}{"limit": 1, "offset": 0}},
		{"应用-可见应用", "GET", "/agent/list", nil, nil},
		{"素材-下载", "GET", "/media/get", map[string]string{"media_id": "probe"}, nil},
	}
	out := []interface{}{}
	for _, p := range probes {
		if ctx.Err() != nil {
			break
		}
		item := map[string]interface{}{"api": p.Name}
		var data []byte
		var err error
		if p.Method == "GET" {
			data, err = d.do(ctx, callSpec{method: "GET", path: p.Path, query: p.Query})
		} else {
			data, err = d.do(ctx, callSpec{method: "POST", path: p.Path, body: p.Body})
		}
		if err == nil {
			item["ok"] = true
			item["sample"] = truncate(string(data), 200)
		} else if de, ok := err.(*DumpError); ok {
			item["ok"] = false
			item["errcode"] = de.Code
			item["reason"] = truncate(de.Msg, 160)
		} else {
			item["ok"] = false
			item["reason"] = err.Error()
		}
		out = append(out, item)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
