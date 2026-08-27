package feishu

import (
	"context"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// identity queries who the token belongs to and what scopes it has.
func (d *Dumper) identity(ctx context.Context) {
	o := d.out

	// 1. user identity (only possible with a user token)
	if d.userToken != "" {
		if m, err := d.CollectObj(ctx, "identity", "GET", "/open-apis/authen/v1/user_info", nil, nil, SupUser); err == nil {
			o.setMeta("user", m)
			d.log("identity", fmt.Sprintf("user: %s", strVal(m["name"])))
		} else {
			d.log("identity", "user_info failed: "+err.Error())
			o.Logf("identity user_info: %v", err)
		}
	}

	// 2. granted scopes (user token)
	if d.userToken != "" {
		if m, err := d.CollectObj(ctx, "identity", "GET", "/open-apis/authen/v1/scopes", nil, nil, SupUser); err == nil {
			scopes := m["scopes"]
			o.setMeta("user_granted_scopes", scopes)
			n := 0
			if arr, ok := scopes.([]interface{}); ok {
				n = len(arr)
			}
			d.log("identity", fmt.Sprintf("user granted scopes: %d", n))
		} else {
			d.log("identity", "scopes query failed: "+err.Error())
			o.Logf("identity scopes: %v", err)
		}
	}

	// 3. application info (reveals requested scopes + admin grant status)
	if d.opt.AppID != "" {
		q := larkcore.QueryParams{}
		q.Set("lang", "zh_cn")
		if m, err := d.CollectObj(ctx, "identity", "GET", "/open-apis/application/v6/applications/"+d.opt.AppID, nil, q, SupTenant); err == nil {
			o.setMeta("app", m)
			if app := subMap(m, "app"); app != nil {
				d.log("identity", fmt.Sprintf("app: %s (id=%s)", strVal(app["app_name"]), strVal(app["app_id"])))
				if sc, ok := app["scopes"].([]interface{}); ok {
					o.setMeta("app_scopes", sc)
					d.log("identity", fmt.Sprintf("app scopes recorded: %d", len(sc)))
				}
			}
		} else {
			o.Logf("identity app_info: %v", err)
		}
	}

	// 4. tenant info (tenant token; works with tenant token)
	if m, err := d.CollectObj(ctx, "identity", "GET", "/open-apis/tenant/v2/tenant/query", nil, nil, SupTenant); err == nil {
		o.setMeta("tenant", m)
		if t := subMap(m, "tenant"); t != nil {
			d.log("identity", fmt.Sprintf("tenant: %s  tag=%s", strVal(t["name"]), strVal(t["tenant_tag"])))
		}
	} else {
		d.log("identity", "tenant query failed: "+err.Error())
	}
}
