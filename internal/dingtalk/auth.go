package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ValidateCredential probes the credential: DingTalk has no introspection
// endpoint, so POST /auth/scopes doubles as the probe — it returns the app's
// granted org scopes and needs no business permission. Any non-auth error
// (403 etc.) still proves the token passed the gateway.
func (d *Dumper) ValidateCredential(ctx context.Context) error {
	if d.opt.AppSecret == "" && d.opt.AccessToken == "" {
		return fmt.Errorf("no credential provided")
	}
	// 先确保 token 可获取（appkey/secret 错误在此暴露）
	if _, err := d.token(ctx, false); err != nil {
		d.reqLog("token acquisition: %v", err)
		return fmt.Errorf("credential rejected: %w", err)
	}
	data, err := d.do(ctx, callSpec{method: "GET", path: "/auth/scopes"})
	if err != nil {
		var de *DumpError
		if errors.As(err, &de) && !invalidTokenCodes[de.Code] {
			// token passed auth; endpoint denied for business reasons
			d.log("auth", fmt.Sprintf("credential valid (probe /auth/scopes -> %s)", de.Error()))
			return nil
		}
		d.reqLog("credential probe /auth/scopes: %v", err)
		return fmt.Errorf("credential rejected: %w", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}
	d.out.SetMeta("org_scopes", root)
	d.log("auth", "credential valid (probe /auth/scopes -> errcode 0)")
	return nil
}

// identity stores the granted org scopes (already captured in the probe).
func (d *Dumper) identity(ctx context.Context) {
	if data, err := d.do(ctx, callSpec{method: "GET", path: "/auth/scopes"}); err == nil {
		var root map[string]interface{}
		if json.Unmarshal(data, &root) == nil {
			d.out.SetMeta("org_scopes", root)
		}
	} else {
		d.reqLog("identity scopes: %v", err)
	}
}

// scopedDepts extracts the authed_dept list from org scopes.
func (d *Dumper) scopedDepts(ctx context.Context) []int64 {
	data, err := d.do(ctx, callSpec{method: "GET", path: "/auth/scopes"})
	if err != nil || !json.Valid(data) {
		return nil
	}
	var root struct {
		AuthOrgScopes struct {
			AuthedDept []int64 `json:"authed_dept"`
		} `json:"auth_org_scopes"`
	}
	if json.Unmarshal(data, &root) != nil {
		return nil
	}
	return root.AuthOrgScopes.AuthedDept
}

// scopedUserIDs extracts the authed_user list from org scopes.
func (d *Dumper) scopedUserIDs(ctx context.Context) []string {
	data, err := d.do(ctx, callSpec{method: "GET", path: "/auth/scopes"})
	if err != nil || !json.Valid(data) {
		return nil
	}
	var root struct {
		AuthOrgScopes struct {
			AuthedUser []string `json:"authed_user"`
		} `json:"auth_org_scopes"`
	}
	if json.Unmarshal(data, &root) != nil {
		return nil
	}
	return root.AuthOrgScopes.AuthedUser
}
