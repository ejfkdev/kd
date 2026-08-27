package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func imTasks() []task {
	g := "im"
	return []task{
		{Group: g, Key: "chats", Desc: "机器人在的群", File: "chats_list", Run: modImChats},
		{Group: g, Key: "chat_details", Footprint: "im/chats", Desc: "群信息(逐群)", Run: modImChatDetails},
		{Group: g, Key: "chat_members", Footprint: "im/chats", Desc: "群成员(逐群)", Run: modImChatMembers},
		{Group: g, Key: "chat_managers", Footprint: "im/chats", Desc: "群管理员(逐群)", Run: modImChatManagers},
		{Group: g, Key: "chat_announcements", Footprint: "im/chats", Desc: "群公告(逐群)", Run: modImChatAnnouncements},
		{Group: g, Key: "chat_pins", Footprint: "im/chats", Desc: "群置顶(逐群)", Run: modImChatPins},
		{Group: g, Key: "messages", Footprint: "im/chats", Desc: "群历史消息(逐群)", Run: modImMessages},
		{Group: g, Key: "message_reactions", Footprint: "im/chats", Desc: "消息表情回复", Run: modImReactions},
		{Group: g, Key: "message_resources", Desc: "消息图片/附件抽取", Run: modImResources},
	}
}

func modImChats(ctx context.Context, d *Dumper) (interface{}, error) {
	return d.memoized("im.chats", func() (interface{}, error) {
		return d.Collect(ctx, ListConf{
			Name: "im_chats", Method: "GET", Path: "/open-apis/im/v1/chats",
			Query:    uidTypeQ(d),
			Supports: SupTenant | SupUser, PageSize: 100,
		})
	})
}

// gather helper: run per-chat collectors once
func collectChatIDs(ctx context.Context, d *Dumper) ([]interface{}, error) {
	chats, err := modImChats(ctx, d)
	if err != nil {
		return nil, err
	}
	return chats.([]interface{}), nil
}

func modImChatDetails(ctx context.Context, d *Dumper) (interface{}, error) {
	chats, err := collectChatIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	base := append([]interface{}{}, chats...)
	for _, cid := range d.ids("chat") {
		base = append(base, map[string]interface{}{"chat_id": cid})
	}
	res := make([]map[string]interface{}, len(base))
	d.ForEach(ctx, d.opt.Workers, len(base), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, _ := base[i].(map[string]interface{})
		cid := strVal(m["chat_id"])
		if cid == "" || seen[cid] {
			return
		}
		seen[cid] = true
		m2, err := d.CollectObj(ctx, "im_chat_details", "GET", "/open-apis/im/v1/chats/"+cid, nil, uidTypeQ(d), SupTenant|SupUser)
		if err != nil {
			d.reqLog("chat %s detail: %v", cid, err)
			return
		}
		res[i] = m2
	})
	var parts []SplitPart
	for i := range base {
		if res[i] == nil {
			continue
		}
		parts = append(parts, SplitPart{SubKey: strVal(res[i]["chat_id"]), Object: res[i]})
	}
	return &SplitResult{SubField: "chat_id", Path: "im/chats/{sub}/detail.json", Parts: parts}, nil
}

func modImChatMembers(ctx context.Context, d *Dumper) (interface{}, error) {
	chats, err := collectChatIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	res := make([][]interface{}, len(chats))
	ids := make([]string, len(chats))
	d.ForEach(ctx, d.opt.Workers, len(chats), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, _ := chats[i].(map[string]interface{})
		cid := strVal(m["chat_id"])
		if cid == "" {
			return
		}
		ids[i] = cid
		q := larkcore.QueryParams{}
		q.Set("member_id_type", d.opt.UserIDType)
		items, err := d.Collect(ctx, ListConf{
			Name: "im_chat_members", Method: "GET", Path: "/open-apis/im/v1/chats/" + cid + "/members",
			Query: q, Supports: SupTenant | SupUser, PageSize: 100,
		})
		if err != nil {
			d.reqLog("chat %s members: %v", cid, err)
			return
		}
		for _, it := range items {
			if mm, ok := it.(map[string]interface{}); ok {
				d.addIDs("user", strVal(mm["member_id"]))
			}
		}
		res[i] = items
	})
	var parts []SplitPart
	for i := range chats {
		if len(res[i]) == 0 || ids[i] == "" {
			continue
		}
		parts = append(parts, SplitPart{SubKey: ids[i], Items: res[i]})
	}
	return &SplitResult{SubField: "chat_id", Path: "im/chats/{sub}/members.json", Parts: parts}, nil
}

func modImChatManagers(ctx context.Context, d *Dumper) (interface{}, error) {
	chats, err := collectChatIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	res := make([][]interface{}, len(chats))
	ids := make([]string, len(chats))
	d.ForEach(ctx, d.opt.Workers, len(chats), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, _ := chats[i].(map[string]interface{})
		cid := strVal(m["chat_id"])
		if cid == "" {
			return
		}
		ids[i] = cid
		items, err := d.Collect(ctx, ListConf{
			Name: "im_chat_managers", Method: "GET", Path: "/open-apis/im/v1/chats/" + cid + "/managers",
			Query: uidTypeQ(d), Supports: SupTenant | SupUser, PageSize: 100,
		})
		if err != nil {
			d.reqLog("chat %s managers: %v", cid, err)
			return
		}
		res[i] = items
	})
	var parts []SplitPart
	for i := range chats {
		if len(res[i]) == 0 || ids[i] == "" {
			continue
		}
		parts = append(parts, SplitPart{SubKey: ids[i], Items: res[i]})
	}
	return &SplitResult{SubField: "chat_id", Path: "im/chats/{sub}/managers.json", Parts: parts}, nil
}

func modImChatAnnouncements(ctx context.Context, d *Dumper) (interface{}, error) {
	chats, err := collectChatIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	res := make([]map[string]interface{}, len(chats))
	ids := make([]string, len(chats))
	d.ForEach(ctx, d.opt.Workers, len(chats), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, _ := chats[i].(map[string]interface{})
		cid := strVal(m["chat_id"])
		if cid == "" {
			return
		}
		ids[i] = cid
		m2, err := d.CollectObj(ctx, "im_chat_announcements", "GET", "/open-apis/im/v1/chats/"+cid+"/announcement", nil, uidTypeQ(d), SupTenant|SupUser)
		if err != nil {
			// docx-typed chats: read the announcement via the docx api instead
			blocks, err2 := d.Collect(ctx, ListConf{
				Name: "chat_announcement_blocks", Method: "GET",
				Path:     "/open-apis/docx/v1/chats/" + cid + "/announcement/blocks",
				Query:    uidTypeQ(d),
				Supports: SupTenant | SupUser, PageSize: 100,
			})
			if err2 != nil || len(blocks) == 0 {
				d.reqLog("chat %s announcement: %v", cid, err)
				return
			}
			m2 = map[string]interface{}{"blocks": blocks}
		}
		res[i] = m2
	})
	var parts []SplitPart
	for i := range chats {
		if len(res[i]) == 0 || ids[i] == "" {
			continue
		}
		parts = append(parts, SplitPart{SubKey: ids[i], Object: res[i]})
	}
	return &SplitResult{SubField: "chat_id", Path: "im/chats/{sub}/announcement.json", Parts: parts}, nil
}

func modImChatPins(ctx context.Context, d *Dumper) (interface{}, error) {
	chats, err := collectChatIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	res := make([][]interface{}, len(chats))
	ids := make([]string, len(chats))
	d.ForEach(ctx, d.opt.Workers, len(chats), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m, _ := chats[i].(map[string]interface{})
		cid := strVal(m["chat_id"])
		if cid == "" {
			return
		}
		ids[i] = cid
		q := larkcore.QueryParams{}
		q.Set("chat_id", cid)
		items, err := d.Collect(ctx, ListConf{
			Name: "im_chat_pins", Method: "GET", Path: "/open-apis/im/v1/pins",
			Query: q, Supports: SupTenant | SupUser, PageSize: 50,
		})
		if err != nil {
			d.reqLog("chat %s pins: %v", cid, err)
			return
		}
		res[i] = items
	})
	var parts []SplitPart
	for i := range chats {
		if len(res[i]) == 0 || ids[i] == "" {
			continue
		}
		parts = append(parts, SplitPart{SubKey: ids[i], Items: res[i]})
	}
	return &SplitResult{SubField: "chat_id", Path: "im/chats/{sub}/pins.json", Parts: parts}, nil
}

func (d *Dumper) imMessagesRaw(ctx context.Context) ([]interface{}, error) {
	v, err := d.memoized("im.messages.raw", func() (interface{}, error) {
		if d.opt.NoMessages {
			return []interface{}{}, nil
		}
		chats, err := collectChatIDs(ctx, d)
		if err != nil {
			return nil, err
		}
		perChat := make([][]interface{}, len(chats))
		d.ForEach(ctx, d.opt.Workers, len(chats), func(i int) {
			m, _ := chats[i].(map[string]interface{})
			cid := strVal(m["chat_id"])
			if cid == "" {
				return
			}
			q := larkcore.QueryParams{}
			q.Set("container_id_type", "chat")
			q.Set("container_id", cid)
			q.Set("sort_type", "ByCreateTimeAsc")
			items, err := d.Collect(ctx, ListConf{
				Name: "im_messages", Method: "GET", Path: "/open-apis/im/v1/messages",
				Query: q, Supports: SupTenant | SupUser, PageSize: 50,
			})
			if err != nil {
				d.log("im_messages", fmt.Sprintf("chat %s: %v", cid, err))
				return
			}
			for _, it := range items {
				if mm, ok := it.(map[string]interface{}); ok {
					d.harvestMessage(mm)
				}
				perChat[i] = append(perChat[i], it)
			}
		})
		var all []interface{}
		for _, pc := range perChat {
			all = append(all, pc...)
		}
		d.log("im_messages", fmt.Sprintf("total %d messages from %d chats", len(all), len(chats)))
		return all, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]interface{}), nil
}

func modImMessages(ctx context.Context, d *Dumper) (interface{}, error) {
	if d.opt.NoMessages {
		return map[string]interface{}{"note": "disabled (-no-messages)"}, nil
	}
	raw, err := d.imMessagesRaw(ctx)
	if err != nil {
		return nil, err
	}
	arr := raw
	var parts []SplitPart
	index := map[string]int{}
	for _, ms := range arr {
		m, ok := ms.(map[string]interface{})
		if !ok {
			continue
		}
		cid := strVal(m["chat_id"])
		if cid == "" {
			cid = "unknown"
		}
		i, ok := index[cid]
		if !ok {
			index[cid] = len(parts)
			parts = append(parts, SplitPart{SubKey: cid})
			i = len(parts) - 1
		}
		parts[i].Items = append(parts[i].Items, ms)
	}
	return &SplitResult{SubField: "chat_id", Path: "im/chats/{sub}/messages.json", Parts: parts}, nil
}

func modImReactions(ctx context.Context, d *Dumper) (interface{}, error) {
	if d.opt.NoReactions {
		return map[string]interface{}{"note": "disabled (-no-reactions)"}, nil
	}
	msgs, err := d.imMessagesRaw(ctx)
	if err != nil {
		return nil, err
	}
	arr := msgs
	results := make([][]interface{}, len(arr))
	var fails, successes int64
	var firstErrMu sync.Mutex
	var firstErr error
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	noteFail := func(err error) {
		n := atomic.AddInt64(&fails, 1)
		firstErrMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		firstErrMu.Unlock()
		if n >= 5 && atomic.LoadInt64(&successes) == 0 {
			cancel()
		}
	}
	d.ForEach(ctx2, d.opt.Workers, len(arr), func(i int) {
		m, ok := arr[i].(map[string]interface{})
		if !ok {
			return
		}
		mid := strVal(m["message_id"])
		mt := strVal(m["msg_type"])
		if mid == "" || mt == "system" || mt == "video_chat" {
			return
		}
		q := uidTypeQ(d)
		items, err := d.Collect(ctx2, ListConf{
			Name: "im_message_reactions", Method: "GET",
			Path:     "/open-apis/im/v1/messages/" + mid + "/reactions",
			Query:    q,
			Supports: SupTenant | SupUser, PageSize: 50,
		})
		if err != nil {
			noteFail(err)
			return
		}
		atomic.AddInt64(&successes, 1)
		for _, it := range items {
			if rm, ok := it.(map[string]interface{}); ok {
				d.addIDs("user", strVal(rm["operator"]))
				if us, ok := rm["users"].([]interface{}); ok {
					for _, u := range us {
						if um, ok2 := u.(map[string]interface{}); ok2 {
							d.addIDs("user", firstNonEmpty(strVal(um["user_id"]), strVal(um["open_id"])))
						}
					}
				}
			}
		}
		if len(items) > 0 {
			results[i] = attach(items, "message_id", mid)
		}
	})
	var parts []SplitPart
	idx := map[string]int{}
	for i := range arr {
		rs := results[i]
		if len(rs) == 0 {
			continue
		}
		m, _ := arr[i].(map[string]interface{})
		cid := strVal(m["chat_id"])
		if cid == "" {
			cid = "unknown"
		}
		j, ok := idx[cid]
		if !ok {
			idx[cid] = len(parts)
			parts = append(parts, SplitPart{SubKey: cid})
			j = len(parts) - 1
		}
		parts[j].Items = append(parts[j].Items, rs...)
	}
	if successes == 0 && firstErr != nil {
		return nil, firstErr
	}
	return &SplitResult{SubField: "chat_id", Path: "im/chats/{sub}/reactions.json", Parts: parts}, nil
}

func modImResources(ctx context.Context, d *Dumper) (interface{}, error) {
	if d.opt.NoDownload {
		return []interface{}{map[string]interface{}{"note": "disabled (-no-download)"}}, nil
	}
	msgs, err := d.imMessagesRaw(ctx)
	if err != nil {
		return nil, err
	}
	arr := msgs
	seen := map[string]bool{}
	var listed []interface{}
	add := func(mid, chatID, key, typ, name string) {
		if key == "" {
			return
		}
		k := mid + "\x00" + key
		if seen[k] {
			return
		}
		seen[k] = true
		d.AddResource("im", key, name, map[string]interface{}{
			"message_id": mid,
			"chat_id":    chatID,
			"type":       typ,
		})
		listed = append(listed, map[string]interface{}{
			"id": key, "name": name, "type": typ,
			"message_id": mid, "chat_id": chatID,
		})
	}
	for _, ms := range arr {
		if ctx.Err() != nil {
			break
		}
		m, ok := ms.(map[string]interface{})
		if !ok {
			continue
		}
		if _, isCount := m["_per_chat_counts"]; isCount {
			continue
		}
		mid := strVal(m["message_id"])
		chatID := strVal(m["chat_id"])
		mt := strVal(m["msg_type"])
		body, _ := m["body"].(map[string]interface{})
		content, _ := body["content"].(string)
		switch mt {
		case "file":
			key, name := fileContentKeyName(content)
			if key == "" {
				key = content
			}
			if name == "" {
				name = strVal(body["file_name"])
			}
			add(mid, chatID, key, "file", name)
		case "image":
			key := strVal(body["image_key"])
			if key == "" {
				key, _ = fileContentKeyName(content)
			}
			add(mid, chatID, key, "image", "")
		case "audio":
			key, name := fileContentKeyName(content)
			if key == "" {
				key = strVal(body["file_key"])
			}
			if name == "" {
				name = strVal(body["file_name"])
			}
			add(mid, chatID, key, "file", name)
		case "media":
			key, name := fileContentKeyName(content)
			if key == "" {
				key = strVal(body["file_key"])
			}
			if name == "" {
				name = strVal(body["file_name"])
			}
			add(mid, chatID, key, "file", name)
		case "post":
			// rich text: per-element keys with the element's own type/name
			postResources(content, func(key, typ, name string) { add(mid, chatID, key, typ, name) })
		}
	}
	return listed, nil
}

// fileContentKeyName parses the JSON-encoded content field of file/audio/media
// messages: it carries the real key and original file name as JSON.
func fileContentKeyName(content string) (string, string) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "{") {
		return content, ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return content, ""
	}
	key := firstNonEmpty(strVal(m["file_key"]), strVal(m["image_key"]), strVal(m["media_key"]), strVal(m["file_token"]))
	name := firstNonEmpty(strVal(m["file_name"]), strVal(m["name"]))
	return key, name
}

// postResources walks the post content JSON and reports every image/file
// element with its own type (file elements carry their original file name).
func postResources(content string, cb func(key, typ, name string)) {
	var parsed interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return
	}
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch vv := v.(type) {
		case map[string]interface{}:
			if k := strVal(vv["image_key"]); k != "" {
				cb(k, "image", "")
			}
			if k := strVal(vv["file_key"]); k != "" {
				cb(k, "file", firstNonEmpty(strVal(vv["file_name"]), strVal(vv["name"])))
			}
			for _, sub := range vv {
				walk(sub)
			}
		case []interface{}:
			for _, one := range vv {
				walk(one)
			}
		}
	}
	walk(parsed)
}
