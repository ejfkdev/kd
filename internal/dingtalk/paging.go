package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PageSpec describes a cursor-paginated JSON POST list endpoint.
// Paths follow DingTalk's response shape: {"result":{"list":[...],"next_cursor":
// N,"has_more":true}, "errcode":0}.
type PageSpec struct {
	Name     string
	Path     string
	Body     func(cursor int64, size int) map[string]interface{}
	ListPath string // default "result.list"
	NextPath string // default "result.next_cursor"
	MorePath string // default "result.has_more"
	Size     int    // default 100; endpoints with smaller caps shrink automatically
	MaxItems int    // 0 = unlimited (falls back to opt.MaxItems)
}

// CollectPages pages through one endpoint, merging list arrays across pages.
func (d *Dumper) CollectPages(ctx context.Context, p PageSpec) ([]interface{}, error) {
	if p.ListPath == "" {
		p.ListPath = "result.list"
	}
	if p.NextPath == "" {
		p.NextPath = "result.next_cursor"
	}
	if p.MorePath == "" {
		p.MorePath = "result.has_more"
	}
	size := p.Size
	if size == 0 {
		size = 100
	}
	maxItems := p.MaxItems
	if maxItems == 0 {
		maxItems = d.opt.MaxItems
	}

	var all []interface{}
	cursor := int64(0)
	for page := 1; ; page++ {
		body := p.Body(cursor, size)
		if body == nil {
			body = map[string]interface{}{}
		}
		if _, ok := body["cursor"]; !ok {
			body["cursor"] = cursor
		}
		if _, ok := body["size"]; !ok {
			body["size"] = size
		}
		data, err := d.do(ctx, callSpec{method: "POST", path: p.Path, body: body})
		if err != nil {
			var de *DumpError
			if errMsg(err, &de) && de.Code == 40018 && size > 10 {
				// 40018 = 分页参数不合法: some endpoints cap size; halve and retry
				next := size / 2
				if next < 10 {
					next = 10
				}
				d.log(p.Name, fmt.Sprintf("size=%d rejected, retrying with %d", size, next))
				size = next
				page--
				continue
			}
			return nil, err
		}
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("%s: decode response: %w", p.Name, err)
		}
		items := pickPath(root, p.ListPath)
		if items == nil {
			if !pickBool(root, p.MorePath) {
				d.logf(p.Name, "page %d: empty (list key absent)", page)
				return all, nil
			}
			return nil, fmt.Errorf("%s: list key %q missing: %.200s", p.Name, p.ListPath, string(data))
		}
		for _, it := range items {
			all = append(all, it)
			if maxItems > 0 && len(all) >= maxItems {
				break
			}
		}
		hasMore := pickBool(root, p.MorePath)
		var next int64
		if v, ok := pickNum(root, p.NextPath); ok {
			next = v
		}
		d.logf(p.Name, "page %d: +%d (total %d)", page, len(items), len(all))
		if maxItems > 0 && len(all) >= maxItems {
			break
		}
		if !hasMore || next <= 0 {
			break
		}
		cursor = next
		if page > 100000 {
			return nil, fmt.Errorf("%s: page loop safety limit", p.Name)
		}
	}
	return all, nil
}

func errMsg(err error, target **DumpError) bool {
	de, ok := err.(*DumpError)
	if ok {
		*target = de
	}
	return ok
}

// pickPath resolves a dotted JSON path like "result.list".
func pickPath(root map[string]interface{}, path string) []interface{} {
	cur := interface{}(root)
	parts := splitDots(path)
	for _, part := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = m[part]
	}
	arr, _ := cur.([]interface{})
	return arr
}

func pickBool(root map[string]interface{}, path string) bool {
	parts := splitDots(path)
	cur := interface{}(root)
	for _, part := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return false
		}
		cur = m[part]
	}
	b, _ := cur.(bool)
	return b
}

func pickNum(root map[string]interface{}, path string) (int64, bool) {
	parts := splitDots(path)
	cur := interface{}(root)
	for _, part := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return 0, false
		}
		cur = m[part]
	}
	switch v := cur.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	}
	return 0, false
}

func splitDots(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// strVal / fmtsafe helpers (mirror the feishu tool's conventions).
func strVal(v interface{}) string {
	s, _ := v.(string)
	return s
}

func numVal(v interface{}) float64 {
	f, _ := v.(float64)
	return f
}

func boolVal(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

func ts() string { return time.Now().Format(time.RFC3339) }
