package wecom

import (
	"context"
	"fmt"
)

func agentTasks() []task {
	g := "agent"
	return []task{
		{Group: g, Key: "apps", Desc: "可见应用列表", Run: modAgentList},
		{Group: g, Key: "app_details", Desc: "应用详情(逐应用)", Run: modAgentDetails},
	}
}

// modAgentList: /agent/list 返回当前 secret 可见的应用。
func modAgentList(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("agent.apps", func() (interface{}, error) {
		root, err := d.getJSON(ctx, "/agent/list", nil)
		if err != nil {
			return nil, err
		}
		list, _ := root["agentlist"].([]interface{})
		if !d.opt.NoDownload {
			for _, it := range list {
				m, _ := it.(map[string]interface{})
				aid := fmt.Sprintf("%v", m["agentid"])
				if logo := strVal(m["square_logo_url"]); logo != "" {
					d.AddResource("agent", aid, "", map[string]interface{}{"url": logo, "name": "logo"})
				}
			}
		}
		return list, nil
	})
}

func modAgentDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	listRaw, err := modAgentList(ctx, d)
	if err != nil {
		return nil, err
	}
	list, _ := listRaw.([]interface{})
	ids := make([]string, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]interface{})
		ids = append(ids, fmt.Sprintf("%v", m["agentid"]))
	}
	res := make([]interface{}, len(ids))
	d.ForEach(ctx, d.opt.Workers, len(ids), func(i int) {
		if ctx.Err() != nil {
			return
		}
		root, err := d.getJSON(ctx, "/agent/get", map[string]string{"agentid": ids[i]})
		if err != nil {
			d.reqLog("agent %s get: %v", ids[i], err)
			return
		}
		res[i] = root
	})
	out := map[string]interface{}{}
	for i := range ids {
		if res[i] != nil {
			out[ids[i]] = res[i]
		}
	}
	return out, nil
}

// agentIDs 供其他模块复用（消息审计等场景暂用不到，保留探测用）。
func (d *Dumper) agentIDs(ctx context.Context) []string {
	listRaw, err := modAgentList(ctx, d)
	if err != nil {
		return nil
	}
	list, _ := listRaw.([]interface{})
	ids := make([]string, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]interface{})
		ids = append(ids, fmt.Sprintf("%v", m["agentid"]))
	}
	return ids
}
