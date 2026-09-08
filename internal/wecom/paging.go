package wecom

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// PageSpec describes a cursor/page_id-paginated JSON POST list endpoint.
// WeCom 分页响应形态不一：{"group_chat_list":[...],"next_cursor":"x"}、
// {"sp_no_list":[...],"next_cursor":"x"}、{"moment_list":[...],"next_cursor":..}、
// {"info_list":[...],"is_last":1,"next_page_id":..} 等，故路径全部可配。
type PageSpec struct {
	Name     string
	Path     string
	Body     func(cursor string, size int) map[string]interface{}
	ListPath string // e.g. "group_chat_list"
	NextPath string // e.g. "next_cursor"; 空 = 无游标字段
	MorePath string // e.g. "has_more"/"is_last"(bool)；空 = 以 next 游标非空判断
	MoreIs   bool   // MorePath 的"还有更多"取值；is_last 这类反向语义用 MoreIs=false
	Size     int    // 默认 100
	MaxItems int    // 0 = unlimited (falls back to opt.MaxItems)
}

// CollectPages pages through one endpoint, merging list arrays across pages.
func (d *Dumper) CollectPages(ctx context.Context, p PageSpec) ([]interface{}, error) {
	size := p.Size
	if size == 0 {
		size = 100
	}
	maxItems := p.MaxItems
	if maxItems == 0 {
		maxItems = d.opt.MaxItems
	}

	var all []interface{}
	cursor := ""
	for page := 1; ; page++ {
		body := p.Body(cursor, size)
		if body == nil {
			body = map[string]interface{}{}
		}
		root, err := d.postJSON(ctx, p.Path, body)
		if err != nil {
			return nil, err
		}
		items := pickPath(root, p.ListPath)
		for _, it := range items {
			all = append(all, it)
			if maxItems > 0 && len(all) >= maxItems {
				break
			}
		}
		d.logf(p.Name, "page %d: +%d (total %d)", page, len(items), len(all))
		if maxItems > 0 && len(all) >= maxItems {
			break
		}
		// 是否还有下一页（is_last 这类反向语义用 MoreIs=false）
		if p.MorePath != "" {
			hasMore := truthyOf(pickAny(root, p.MorePath)) == p.MoreIs
			if !hasMore {
				break
			}
		}
		next := cursorStr(pickAny(root, p.NextPath))
		if next == "" || next == cursor {
			break
		}
		cursor = next
		if page > 100000 {
			return nil, fmt.Errorf("%s: page loop safety limit", p.Name)
		}
	}
	return all, nil
}

// pickPath resolves a dotted JSON path to an array.
func pickPath(root map[string]interface{}, path string) []interface{} {
	arr, _ := pickAny(root, path).([]interface{})
	return arr
}

func pickBool(root map[string]interface{}, path string) bool {
	return truthyOf(pickAny(root, path))
}

// truthyOf 兼容 bool 与数值 0/1（企业微信 is_last 等字段为数值）。
func truthyOf(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return false
}

// cursorStr 把游标值统一成字符串（多为 string，个别接口为数值）。
func cursorStr(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return ""
}

// pickAny resolves a dotted JSON path to the raw value.
func pickAny(root map[string]interface{}, path string) interface{} {
	if path == "" {
		return nil
	}
	cur := interface{}(root)
	for _, part := range splitDots(path) {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
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

// strVal / numVal helpers (mirror the other tools' conventions).
func strVal(v interface{}) string {
	s, _ := v.(string)
	return s
}

func numVal(v interface{}) float64 {
	f, _ := v.(float64)
	return f
}

func ts() string { return time.Now().Format(time.RFC3339) }
