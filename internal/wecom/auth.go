package wecom

import (
	"context"
	"errors"
	"fmt"
)

// ValidateCredential probes the credential. GET /get_api_domain_ip 不需要任何
// 业务权限，是理想探针；即便它意外失败，非鉴权类错误码也证明 token 有效。
func (d *Dumper) ValidateCredential(ctx context.Context) error {
	if d.opt.CorpSecret == "" && d.opt.AccessToken == "" {
		return fmt.Errorf("no credential provided")
	}
	// 先确保 token 可获取（corpid/corpsecret 错误在此暴露）
	if _, err := d.token(ctx, false); err != nil {
		d.reqLog("token acquisition: %v", err)
		return fmt.Errorf("credential rejected: %w", err)
	}
	root, err := d.getJSON(ctx, "/get_api_domain_ip", nil)
	if err != nil {
		var de *DumpError
		if errors.As(err, &de) && !invalidTokenCodes[de.Code] {
			d.log("auth", fmt.Sprintf("credential valid (probe /get_api_domain_ip -> %s)", de.Error()))
			return nil
		}
		d.reqLog("credential probe /get_api_domain_ip: %v", err)
		return fmt.Errorf("credential rejected: %w", err)
	}
	d.out.SetMeta("api_domain_ip", root["ip_list"])
	d.log("auth", "credential valid (probe /get_api_domain_ip -> errcode 0)")
	return nil
}

// identity captures what the credential itself reveals: corp id and the app
// list visible to this secret (agent/list works for any self-built app).
func (d *Dumper) identity(ctx context.Context) {
	if d.opt.CorpID != "" {
		d.out.SetMeta("corpid", d.opt.CorpID)
	}
	if root, err := d.getJSON(ctx, "/agent/list", nil); err == nil {
		d.out.SetMeta("agent_list", root["agentlist"])
		if n, ok := root["agentlist"].([]interface{}); ok {
			d.log("identity", fmt.Sprintf("%d visible apps captured", len(n)))
		}
	} else {
		d.reqLog("identity agent/list: %v", err)
	}
}
