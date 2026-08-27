package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// ListConf describes how to page through one list endpoint.
type ListConf struct {
	Name   string // module name for logs
	Method string
	Path   string
	Paths  map[string]string
	Query  larkcore.QueryParams // static query params
	Body   func(pageToken string, pageNum int) interface{}
	Watch  func(pageNum int, items []interface{}) // optional per-page hook

	// Response JSON key holding the array inside "data". Default "items".
	ListKey string
	// Response pagination fields inside "data".
	HasMoreKey   string
	NextTokenKey string

	// Query param name receiving the next page token.
	PageTokenQuery string
	PageSize       int

	Supports Supports
	MaxItems int // 0 = unlimited
	Quiet    bool
}

// Collect pages through an endpoint and merges every page array into one slice.
func (d *Dumper) Collect(ctx context.Context, c ListConf) ([]interface{}, error) {
	if c.ListKey == "" {
		c.ListKey = "items"
	}
	if c.HasMoreKey == "" {
		c.HasMoreKey = "has_more"
	}
	if c.NextTokenKey == "" {
		c.NextTokenKey = "page_token"
	}
	if c.PageTokenQuery == "" {
		c.PageTokenQuery = "page_token"
	}
	if c.PageSize == 0 {
		c.PageSize = 100
	}
	if c.MaxItems == 0 {
		c.MaxItems = d.opt.MaxItems // global cap
	}
	return d.collectWith(ctx, c, c.PageSize, 0)
}

// collectWith implements the paging loop; some endpoints reject large
// page_size with 99992402 (field validation), so retry with halved sizes.
func (d *Dumper) collectWith(ctx context.Context, c ListConf, pageSize, depth int) ([]interface{}, error) {
	var all []interface{}
	pageToken := ""
	for page := 1; ; page++ {
		q := larkcore.QueryParams{}
		for k, vs := range c.Query {
			q[k] = vs
		}
		q.Set("page_size", strconv.Itoa(pageSize))
		if pageToken != "" {
			q.Set(c.PageTokenQuery, pageToken)
		}

		var body interface{}
		if c.Body != nil {
			body = c.Body(pageToken, page)
		}

		resp, err := d.do(ctx, CallSpec{
			Method:   c.Method,
			Path:     c.Path,
			Paths:    c.Paths,
			Query:    q,
			Body:     body,
			Supports: c.Supports,
			Quiet:    c.Quiet,
		})
		if err != nil {
			var fe *FeishuError
			if errors.As(err, &fe) && fe.Code == 99992402 && pageSize > 10 && depth < 3 {
				next := pageSize / 2
				if next < 10 {
					next = 10
				}
				d.log(c.Name, fmt.Sprintf("page_size=%d rejected (field validation), retrying with %d", pageSize, next))
				d.reqLog("%s: page_size=%d rejected (99992402), retry with %d", c.Name, pageSize, next)
				return d.collectWith(ctx, c, next, depth+1)
			}
			return nil, err
		}

		root, err := decodeMap(resp.RawBody)
		if err != nil {
			return nil, fmt.Errorf("%s: decode response: %w", c.Name, err)
		}
		data := subMap(root, "data")

		itemsRaw := pickList(data, c.ListKey)
		if itemsRaw == nil && c.ListKey != "items" {
			// some endpoints put the array under "items" regardless of docs; tolerate it
			itemsRaw = pickList(data, "items")
		}
		if itemsRaw == nil {
			// empty pages sometimes omit the list key entirely
			if data == nil || !boolVal(data[c.HasMoreKey]) {
				d.logf(c.Name, "page %d: empty (list key absent)", page)
				return all, nil
			}
			return nil, fmt.Errorf("%s: list key %q not found in response: %s", c.Name, c.ListKey, limitStr(string(resp.RawBody), 200))
		}
		for _, it := range itemsRaw {
			all = append(all, it)
			if c.MaxItems > 0 && len(all) >= c.MaxItems {
				break
			}
		}
		if c.Watch != nil {
			c.Watch(page, itemsRaw)
		}

		hasMore := boolVal(data[c.HasMoreKey])
		next := ""
		if tv, ok := data[c.NextTokenKey].(string); ok {
			next = tv
		}
		d.logf(c.Name, "page %d: +%d (total %d)", page, len(itemsRaw), len(all))

		if c.MaxItems > 0 && len(all) >= c.MaxItems {
			d.logf(c.Name, "reached max-items cap %d", c.MaxItems)
			break
		}
		if !hasMore || next == "" {
			break
		}
		pageToken = next
		if page > 100000 {
			return nil, fmt.Errorf("%s: page loop safety limit reached", c.Name)
		}
	}
	return all, nil
}

// CollectObj fetches a single-object endpoint and returns its "data" map.
func (d *Dumper) CollectObj(ctx context.Context, name string, method, path string, paths map[string]string, query larkcore.QueryParams, supports Supports) (map[string]interface{}, error) {
	resp, err := d.do(ctx, CallSpec{Method: method, Path: path, Paths: paths, Query: query, Supports: supports})
	if err != nil {
		return nil, err
	}
	root, err := decodeMap(resp.RawBody)
	if err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", name, err)
	}
	return subMap(root, "data"), nil
}

// CollectPayload is like CollectObj but returns the full root object
// (used where "data" is not the wrapper), e.g. scalar string payloads.
func (d *Dumper) CollectRoot(ctx context.Context, name string, method, path string, paths map[string]string, query larkcore.QueryParams, body interface{}, supports Supports) (map[string]interface{}, error) {
	resp, err := d.do(ctx, CallSpec{Method: method, Path: path, Paths: paths, Query: query, Body: body, Supports: supports})
	if err != nil {
		return nil, err
	}
	root, err := decodeMap(resp.RawBody)
	if err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", name, err)
	}
	return root, nil
}

func decodeMap(raw []byte) (map[string]interface{}, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func subMap(m map[string]interface{}, key string) map[string]interface{} {
	if v, ok := m[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

// pickList extracts a JSON array from data[key], tolerating []interface{} and []json.RawMessage forms produced by Unmarshal.
func pickList(data map[string]interface{}, key string) []interface{} {
	if data == nil {
		return nil
	}
	v, ok := data[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	return arr
}

func boolVal(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

func floatVal(v interface{}) float64 {
	f, _ := v.(float64)
	return f
}

func strVal(v interface{}) string {
	s, _ := v.(string)
	return s
}

func limitStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// mkErr builds the module error object stored in the dump.
func mkErr(err error) map[string]interface{} {
	return map[string]interface{}{"error": err.Error()}
}
