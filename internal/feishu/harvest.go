package feishu

import (
	"regexp"
	"strings"
)

// ID pools collect resource ids harvested from data the credential CAN read
// (message bodies, links, members) so that detail/query endpoints can later be
// used for resources whose list APIs are not accessible.
//
// kinds: user(open_id), chat, doc, sheet, bitable, wiki

var (
	docURLRe     = regexp.MustCompile(`(?:docx|docs|sheets|base|wiki|slides|mindnotes|file)/([A-Za-z0-9_-]{12,64})`)
	docKindRe    = regexp.MustCompile(`/(docx|docs|sheets|base|wiki|slides|mindnotes|file)/`)
	openIDRe     = regexp.MustCompile(`\b(ou_[A-Za-z0-9]{8,64})\b`)
	unionIDRe    = regexp.MustCompile(`\b(on_[A-Za-z0-9]{8,64})\b`)
	chatIDRe     = regexp.MustCompile(`\b(oc_[A-Za-z0-9]{8,64})\b`)
	mentionsIDRe = regexp.MustCompile(`"?(?:open_id|user_id|openId|userId)"?\s*[:=]\s*"?(ou_[A-Za-z0-9]{8,64})"?`)
)

// addIDs registers harvested ids under a kind (deduped, source-labeled).
func (d *Dumper) addIDs(kind string, ids ...string) {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	if d.idPool == nil {
		d.idPool = map[string]map[string]string{}
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if d.idPool[kind] == nil {
			d.idPool[kind] = map[string]string{}
		}
		if _, ok := d.idPool[kind][id]; !ok {
			d.idPool[kind][id] = "harvested"
		}
	}
}

// ids returns all harvested ids of a kind.
func (d *Dumper) ids(kind string) []string {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	var out []string
	for id := range d.idPool[kind] {
		out = append(out, id)
	}
	return out
}

func (d *Dumper) poolKinds() map[string]int {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()
	out := map[string]int{}
	for k, v := range d.idPool {
		out[k] = len(v)
	}
	return out
}

// harvestMessage extracts user/chat/doc ids from one message:
// sender, mentions, share_chat refs and doc/sheet/base/wiki/file links.
func (d *Dumper) harvestMessage(m map[string]interface{}) {
	if sender, ok := m["sender"].(map[string]interface{}); ok {
		if sid := strVal(sender["id"]); sid != "" {
			id := strVal(sender["sender_id"])
			if id == "" {
				id = sid
			}
			if id != "" {
				d.addIDs("user", id)
			}
		}
	}
	body, _ := m["body"].(map[string]interface{})
	content := strVal(body["content"])
	if content == "" {
		return
	}
	for _, uid := range openIDRe.FindAllStringSubmatch(content, -1) {
		d.addIDs("user", uid[1])
	}
	for _, uid := range unionIDRe.FindAllStringSubmatch(content, -1) {
		d.addIDs("user", uid[1])
	}
	for _, uid := range mentionsIDRe.FindAllStringSubmatch(content, -1) {
		d.addIDs("user", uid[1])
	}
	for _, cid := range chatIDRe.FindAllStringSubmatch(content, -1) {
		d.addIDs("chat", cid[1])
	}
	// doc links: /docx/<tok> /sheets/<tok> /base/<tok> /wiki/<tok> /file/<tok> ...
	for _, hit := range docKindRe.FindAllStringSubmatchIndex(content, -1) {
		if len(hit) < 4 {
			continue
		}
		seg := content[hit[2]:hit[3]]
		startTok := hit[1]
		end := strings.IndexAny(content[startTok:], "/?&# \"')\n")
		if end < 0 {
			end = len(content) - startTok
		}
		tok := content[startTok : startTok+end]
		if len(tok) < 12 || len(tok) > 64 {
			continue
		}
		switch seg {
		case "file":
			d.AddResource("drive", tok, "", map[string]interface{}{"type": "file", "source": "message-link"})
		case "docx", "docs":
			d.addIDs("doc", tok)
		case "sheets":
			d.addIDs("sheet", tok)
		case "base":
			d.addIDs("bitable", tok)
		case "wiki":
			d.addIDs("wiki", tok)
		}
	}
}
