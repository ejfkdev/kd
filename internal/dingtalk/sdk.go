package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	attendance "github.com/alibabacloud-go/dingtalk/attendance_1_0"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	contact "github.com/alibabacloud-go/dingtalk/contact_1_0"
	workflow "github.com/alibabacloud-go/dingtalk/workflow_1_0"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
)

// SDK-first policy: the official alibabacloud-go/dingtalk SDK is used whenever
// it models the data surface (new api.dingtalk.com 1.0/2.0 APIs); the legacy
// oapi.dingtalk.com topapi endpoints remain the fallback for surfaces the SDK
// does not cover (contact dept/user enumeration is NOT in contact_1_0).

// sdkConfig builds the darabonba config used by every SDK client.
func (d *Dumper) sdkConfig() *openapi.Config {
	cfg := &openapi.Config{}
	cfg.SetProtocol("https")
	if d.proxy != nil {
		cfg.SetHttpProxy(d.proxy.String())
	}
	return cfg
}

// sdkError converts a darabonba SDK error to a *DumpError, digging the
// {"code": ..., "message": ...} body out of the tea error when possible.
func sdkError(respBody interface{}, err error) error {
	if err == nil {
		return nil
	}
	var sdkErr *tea.SDKError
	if errors.As(err, &sdkErr) {
		var probe struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		raw := ""
		if sdkErr.Data != nil {
			raw = *sdkErr.Data
		}
		status := 0
		if sdkErr.StatusCode != nil {
			status = *sdkErr.StatusCode
		}
		if json.Unmarshal([]byte(raw), &probe) == nil && probe.Code != "" {
			return fmt.Errorf("sdk [%d] %s: %s (body %.300s)", status, probe.Code, probe.Message, raw)
		}
		if sdkErr.Message != nil {
			return fmt.Errorf("sdk [%d] %s (body %.300s)", status, *sdkErr.Message, raw)
		}
		return fmt.Errorf("sdk error: %v", err)
	}
	return err
}

// bodyToMap serializes a generated response body back to a generic map.
func bodyToMap(body interface{}) map[string]interface{} {
	data, err := json.Marshal(body)
	if err != nil {
		return map[string]interface{}{}
	}
	var m map[string]interface{}
	if json.Unmarshal(data, &m) != nil {
		return map[string]interface{}{}
	}
	return m
}

// OrgAuthInfo (SDK 首选)：企业应用授权信息 —— 权限接口优先访问。
func (d *Dumper) OrgAuthInfo(ctx context.Context) (map[string]interface{}, error) {
	tok, err := d.token(ctx, false)
	if err != nil {
		return nil, err
	}
	c, err := contact.NewClient(d.sdkConfig())
	if err != nil {
		return nil, err
	}
	resp, err := c.GetOrgAuthInfoWithOptions(&contact.GetOrgAuthInfoRequest{},
		&contact.GetOrgAuthInfoHeaders{XAcsDingtalkAccessToken: tea.String(tok)},
		&util.RuntimeOptions{})
	if err != nil {
		return nil, sdkError(nil, err)
	}
	if resp.Body == nil {
		return nil, fmt.Errorf("empty body")
	}
	return bodyToMap(resp.Body), nil
}

// ListProcessInstanceIDs (SDK 首选)：审批实例 id 列表，返回值 (ids, nextToken)。
func (d *Dumper) ListProcessInstanceIDs(ctx context.Context, maxResults int64, startTime, endTime, nextToken *int64) ([]string, int64, error) {
	tok, err := d.token(ctx, false)
	if err != nil {
		return nil, 0, err
	}
	c, err := workflow.NewClient(d.sdkConfig())
	if err != nil {
		return nil, 0, err
	}
	req := &workflow.ListProcessInstanceIdsRequest{
		MaxResults: tea.Int64(maxResults),
	}
	if startTime != nil {
		req.StartTime = startTime
	}
	if endTime != nil {
		req.EndTime = endTime
	}
	if nextToken != nil && *nextToken > 0 {
		req.NextToken = nextToken
	}
	resp, err := c.ListProcessInstanceIdsWithOptions(req,
		&workflow.ListProcessInstanceIdsHeaders{XAcsDingtalkAccessToken: tea.String(tok)},
		&util.RuntimeOptions{})
	if err != nil {
		return nil, 0, sdkError(nil, err)
	}
	if resp.Body == nil || resp.Body.Result == nil {
		return nil, 0, nil
	}
	var ids []string
	for _, id := range resp.Body.Result.List {
		if id != nil {
			ids = append(ids, *id)
		}
	}
	var next int64
	if resp.Body.Result.NextToken != nil {
		next, _ = strconv.ParseInt(*resp.Body.Result.NextToken, 10, 64)
	}
	return ids, next, nil
}

// GetProcessInstance (SDK 首选)：审批实例详情。
func (d *Dumper) GetProcessInstance(ctx context.Context, instanceID string) (map[string]interface{}, error) {
	tok, err := d.token(ctx, false)
	if err != nil {
		return nil, err
	}
	c, err := workflow.NewClient(d.sdkConfig())
	if err != nil {
		return nil, err
	}
	resp, err := c.GetProcessInstanceWithOptions(&workflow.GetProcessInstanceRequest{ProcessInstanceId: tea.String(instanceID)},
		&workflow.GetProcessInstanceHeaders{XAcsDingtalkAccessToken: tea.String(tok)},
		&util.RuntimeOptions{})
	if err != nil {
		return nil, sdkError(nil, err)
	}
	if resp.Body == nil {
		return nil, fmt.Errorf("empty body")
	}
	return bodyToMap(resp.Body), nil
}

// AttendanceSimpleGroups (SDK 首选)：考勤组列表。
// 注意：该响应没有分页 token，只有 groups + hasMore。
func (d *Dumper) AttendanceSimpleGroups(ctx context.Context, maxResults int32) ([]map[string]interface{}, bool, error) {
	tok, err := d.token(ctx, false)
	if err != nil {
		return nil, false, err
	}
	c, err := attendance.NewClient(d.sdkConfig())
	if err != nil {
		return nil, false, err
	}
	resp, err := c.GetSimpleGroupsWithOptions(&attendance.GetSimpleGroupsRequest{MaxResults: tea.Int32(maxResults)},
		&attendance.GetSimpleGroupsHeaders{XAcsDingtalkAccessToken: tea.String(tok)},
		&util.RuntimeOptions{})
	if err != nil {
		return nil, false, sdkError(nil, err)
	}
	if resp.Body == nil || resp.Body.Result == nil {
		return nil, false, nil
	}
	var groups []map[string]interface{}
	for _, g := range resp.Body.Result.Groups {
		groups = append(groups, bodyToMap(g))
	}
	return groups, resp.Body.Result.HasMore != nil && *resp.Body.Result.HasMore, nil
}

// CheckinRecordsByUsers (SDK 首选)：按用户批量查打卡记录（时间区间 + nextToken）。
func (d *Dumper) CheckinRecordsByUsers(ctx context.Context, maxResults, startTime, endTime int64, userIds []string, nextToken *int64) ([]map[string]interface{}, int64, error) {
	tok, err := d.token(ctx, false)
	if err != nil {
		return nil, 0, err
	}
	c, err := attendance.NewClient(d.sdkConfig())
	if err != nil {
		return nil, 0, err
	}
	req := &attendance.GetCheckinRecordByUserRequest{
		MaxResults: tea.Int64(maxResults),
		StartTime:  tea.Int64(startTime),
		EndTime:    tea.Int64(endTime),
	}
	if len(userIds) > 0 {
		ids := make([]*string, 0, len(userIds))
		for _, u := range userIds {
			u := u
			ids = append(ids, &u)
		}
		req.UserIdList = ids
	}
	if nextToken != nil && *nextToken > 0 {
		req.NextToken = nextToken
	}
	resp, err := c.GetCheckinRecordByUserWithOptions(req,
		&attendance.GetCheckinRecordByUserHeaders{XAcsDingtalkAccessToken: tea.String(tok)},
		&util.RuntimeOptions{})
	if err != nil {
		return nil, 0, sdkError(nil, err)
	}
	if resp.Body == nil || resp.Body.Result == nil {
		return nil, 0, nil
	}
	var recs []map[string]interface{}
	for _, r := range resp.Body.Result.PageList {
		recs = append(recs, bodyToMap(r))
	}
	var next int64
	if resp.Body.Result.NextToken != nil {
		next = *resp.Body.Result.NextToken
	}
	return recs, next, nil
}
