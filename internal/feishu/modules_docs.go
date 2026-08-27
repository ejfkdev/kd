package feishu

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func docsTasks() []task {
	// wiki runs before the content modules: its nodes reveal docx/sheet/bitable
	// tokens that feed the content pools even when the drive list is denied.
	g := "docs"
	return []task{
		{Group: g, Key: "drive_files", Desc: "云空间文件(递归)", Run: modDriveFiles},
		{Group: g, Key: "wiki_spaces", Desc: "知识库", Run: modWiki},
		{Group: g, Key: "file_metas", Desc: "文件元信息(批量)", Run: modDriveFileMetas},
		{Group: g, Key: "file_comments", Desc: "文件评论(逐文件)", Run: modDriveComments},
		{Group: g, Key: "file_permissions", Desc: "文件权限(逐文件)", Run: modDrivePermissions},
		{Group: g, Key: "docx_documents", Desc: "文档内容", Run: modDocxDocuments},
		{Group: g, Key: "sheets_spreadsheets", Desc: "电子表格内容", Run: modSheets},
		{Group: g, Key: "bitable_apps", Desc: "多维表格内容", Run: modBitable},
	}
}

// driveIndex is the result of the recursive file walk, shared between doc modules.
type driveIndex struct {
	files     []interface{}
	byToken   map[string]map[string]interface{}
	byType    map[string][]string
	rootToken string
	folders   []string
}

func walkDrive(ctx context.Context, d *Dumper) (*driveIndex, error) {
	idx := &driveIndex{
		byToken: map[string]map[string]interface{}{},
		byType:  map[string][]string{},
	}

	// folder_token == "" means the root folder of the current identity's drive
	queue := []string{""}
	visited := map[string]bool{}
	depth := 0
	for len(queue) > 0 {
		if depth > 500 {
			return nil, fmt.Errorf("drive walk depth safety limit reached")
		}
		results := make([][]interface{}, len(queue))
		nextOf := make([][]string, len(queue))
		d.ForEach(ctx, d.opt.Workers, len(queue), func(i int) {
			folder := queue[i]
			if ctx.Err() != nil || visited[folder] {
				if !visited[folder] {
					visited[folder] = true
				}
				return
			}
			visited[folder] = true
			q := larkcore.QueryParams{}
			if folder != "" {
				q.Set("folder_token", folder)
			}
			q.Set("order_by", "EditedTime")
			q.Set("direction", "DESC")
			items, err := d.Collect(ctx, ListConf{
				Name: "drive_files", Method: "GET", Path: "/open-apis/drive/v1/files",
				Query: q, Supports: SupUser | SupTenant, PageSize: 200,
				ListKey: "files", NextTokenKey: "next_page_token",
			})
			if err != nil {
				d.log("drive", fmt.Sprintf("folder %s: %v", folder, err))
				return
			}
			results[i] = items
			for _, it := range items {
				m2, _ := it.(map[string]interface{})
				if strVal(m2["type"]) == "folder" {
					nextOf[i] = append(nextOf[i], strVal(m2["token"]))
				}
			}
		})
		var next []string
		for i := range queue {
			for _, it := range results[i] {
				m2, _ := it.(map[string]interface{})
				tok := strVal(m2["token"])
				if tok == "" {
					continue
				}
				if _, dup := idx.byToken[tok]; dup {
					continue
				}
				nm := make(map[string]interface{}, len(m2)+1)
				for k, v := range m2 {
					nm[k] = v
				}
				if strVal(nm["parent_token"]) == "" {
					nm["parent_token"] = queue[i]
				}
				idx.byToken[tok] = nm
				idx.files = append(idx.files, nm)
				typ := strVal(m2["type"])
				idx.byType[typ] = append(idx.byType[typ], tok)
				if typ == "file" || typ == "media" {
					if !d.opt.NoDownload {
						d.AddResource("drive", tok, strVal(m2["name"]), map[string]interface{}{"type": typ})
					}
				}
			}
			for _, t2 := range nextOf[i] {
				if !visited[t2] {
					next = append(next, t2)
				}
			}
			d.logf("drive", "folder %s: %d children", queue[i], len(results[i]))
		}
		queue = next
		depth++
	}
	return idx, nil
}

func modDriveFiles(ctx context.Context, d *Dumper) (interface{}, error) {
	idx, err := walkDrive(ctx, d)
	if err != nil {
		return nil, err
	}
	return idx.files, nil
}

// driveIndexIfAny returns the walk result or an empty index when the drive list
// API is not accessible (modules then fall back to harvested/pooled tokens).
func driveIndexIfAny(d *Dumper, ctx context.Context) *driveIndex {
	idx, err := driveFileTokens(d, ctx)
	if err != nil {
		return &driveIndex{byToken: map[string]map[string]interface{}{}, byType: map[string][]string{}}
	}
	return idx
}

func driveFileTokens(d *Dumper, ctx context.Context) (*driveIndex, error) {
	if d.driveIdx == nil {
		idx, err := walkDrive(ctx, d)
		if err != nil {
			return nil, err
		}
		d.driveIdx = idx
	}
	return d.driveIdx, nil
}

// modDriveFileMetas batch-fetches file metadata via POST /drive/v1/metas/batch_query
// (there is no single-file GET meta endpoint in this API generation).
func modDriveFileMetas(ctx context.Context, d *Dumper) (interface{}, error) {
	idx, err := driveFileTokens(d, ctx)
	if err != nil {
		return nil, err
	}
	var toks []string
	for tok := range idx.byToken {
		toks = append(toks, tok)
	}
	const chunk = 100
	n := (len(toks) + chunk - 1) / chunk
	var mu sync.Mutex
	out := map[string]interface{}{}
	d.ForEach(ctx, d.opt.Workers, n, func(bi int) {
		i := bi * chunk
		end := i + chunk
		if end > len(toks) {
			end = len(toks)
		}
		docs := make([]map[string]interface{}, 0, end-i)
		for _, tok := range toks[i:end] {
			entry := idx.byToken[tok]
			typ := strVal(entry["type"])
			docs = append(docs, map[string]interface{}{"doc_token": tok, "doc_type": metaType(typ)})
		}
		body := map[string]interface{}{"request_docs": docs, "with_url": true}
		resp, err := d.do(ctx, CallSpec{
			Method: "POST", Path: "/open-apis/drive/v1/metas/batch_query",
			Query: uidTypeQ(d), Body: body, Supports: SupUser | SupTenant,
		})
		if err != nil {
			d.reqLog("drive metas batch %d: %v", bi, err)
			return
		}
		root, derr := decodeMap(resp.RawBody)
		if derr != nil {
			d.reqLog("drive metas batch %d: %v", bi, derr)
			return
		}
		data := subMap(root, "data")
		mu.Lock()
		defer mu.Unlock()
		if metas, ok := data["metas"].([]interface{}); ok {
			for _, mm := range metas {
				m2, _ := mm.(map[string]interface{})
				if t := strVal(m2["doc_token"]); t != "" {
					out[t] = m2
				}
			}
			return
		}
		if failed, ok := data["failed_list"].([]interface{}); ok {
			for _, f := range failed {
				fm, _ := f.(map[string]interface{})
				t := strVal(fm["token"])
				if t == "" {
					t = strVal(fm["doc_token"])
				}
				if t != "" {
					d.reqLog("drive meta %s: failed_list entry type=%s", t, strVal(fm["type"]))
				}
			}
		}
	})
	return out, nil
}

func errMsg(m map[string]interface{}) string {
	if e, ok := m["error"].(string); ok {
		return e
	}
	return "batch query failed"
}

// metaType maps a drive listing type to the metas/batch_query doc_type param.
func metaType(t string) string {
	switch t {
	case "docx", "sheet", "bitable", "file", "folder", "mindnote", "shortcut", "slides", "doc", "wiki":
		return t
	default:
		return "file"
	}
}

func fileTypeParam(t string) string {
	switch t {
	case "docx", "doc":
		return "docx"
	case "sheet":
		return "sheet"
	case "bitable":
		return "bitable"
	case "file":
		return "file"
	case "mindnote":
		return "mindnote"
	}
	return ""
}

func modDriveComments(ctx context.Context, d *Dumper) (interface{}, error) {
	idx, err := driveFileTokens(d, ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	for _, typ := range []string{"docx", "sheet", "bitable", "file"} {
		for _, tok := range idx.byType[typ] {
			if ctx.Err() != nil {
				return out, nil
			}
			q := larkcore.QueryParams{}
			q.Set("file_type", fileTypeParam(typ))
			items, err := d.Collect(ctx, ListConf{
				Name: "drive_file_comments", Method: "GET", Path: "/open-apis/drive/v1/files/" + tok + "/comments",
				Query: q, Supports: SupUser | SupTenant, PageSize: 100,
			})
			if err != nil {
				d.reqLog("drive %s comments: %v", tok, err)
				continue
			}
			out[tok] = items
		}
	}
	return out, nil
}

func modDrivePermissions(ctx context.Context, d *Dumper) (interface{}, error) {
	idx, err := driveFileTokens(d, ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	for _, typ := range []string{"docx", "sheet", "bitable", "file", "mindnote"} {
		for _, tok := range idx.byType[typ] {
			if ctx.Err() != nil {
				return out, nil
			}
			ft := fileTypeParam(typ)
			entry := map[string]interface{}{}
			// public permission setting
			q := larkcore.QueryParams{}
			q.Set("type", ft)
			if m, err := d.CollectObj(ctx, "drive_file_permissions", "GET", "/open-apis/drive/v1/permissions/"+tok+"/public", nil, q, SupUser|SupTenant); err == nil {
				entry["public"] = m
			} else {
				d.reqLog("drive %s public permission: %v", tok, err)
			}
			// collaborator list
			q2 := uidTypeQ(d)
			q2.Set("type", ft)
			if items, err := d.Collect(ctx, ListConf{
				Name: "drive_file_permissions", Method: "GET", Path: "/open-apis/drive/v1/permissions/" + tok + "/members",
				Query: q2, Supports: SupUser | SupTenant, PageSize: 100,
			}); err == nil {
				entry["members"] = items
			} else {
				d.reqLog("drive %s permission members: %v", tok, err)
			}
			if len(entry) == 0 {
				continue
			}
			out[tok] = entry
		}
	}
	return out, nil
}

func modDocxDocuments(ctx context.Context, d *Dumper) (interface{}, error) {
	idx := driveIndexIfAny(d, ctx)
	toks := unionStrings(idx.byType["docx"], d.ids("doc"))
	var parts []SplitPart
	for _, tok := range toks {
		if ctx.Err() != nil {
			break
		}
		m, err := d.CollectObj(ctx, "docx_documents", "GET", "/open-apis/docx/v1/documents/"+tok, nil, nil, SupUser|SupTenant)
		if err != nil {
			d.reqLog("docx %s: %v", tok, err)
			continue
		}
		entry := map[string]interface{}{}
		doc, _ := m["document"].(map[string]interface{})
		if doc != nil && !d.opt.NoDownload {
			var imgToks []string
			scanImages(doc, &imgToks)
			for _, it := range imgToks {
				d.AddResource("docx", it, "", map[string]interface{}{"document_token": tok})
			}
		}
		entry["document"] = doc
		if m2, err := d.CollectObj(ctx, "docx_documents", "GET", "/open-apis/docx/v1/documents/"+tok+"/raw_content", nil, nil, SupUser|SupTenant); err == nil {
			entry["raw_content"] = m2["content"]
		}
		parts = append(parts, SplitPart{SubKey: tok, Object: entry})
	}
	return &SplitResult{SubField: "token", Path: "docs/documents/{sub}.json", Parts: parts}, nil
}

func modSheets(ctx context.Context, d *Dumper) (interface{}, error) {
	idx := driveIndexIfAny(d, ctx)
	toks := unionStrings(idx.byType["sheet"], d.ids("sheet"))
	var parts []SplitPart
	for _, tok := range toks {
		if ctx.Err() != nil {
			break
		}
		meta, err := d.CollectObj(ctx, "sheets", "GET", "/open-apis/sheets/v2/spreadsheets/"+tok+"/metainfo", nil, nil, SupUser|SupTenant)
		if err != nil {
			d.reqLog("sheet %s: %v", tok, err)
			continue
		}
		entry := map[string]interface{}{}
		entry["spreadsheet"] = meta
		var sheetIDs []string
		if shs, ok := meta["sheets"].([]interface{}); ok {
			for _, sh := range shs {
				sm, _ := sh.(map[string]interface{})
				sid := strVal(sm["sheetId"])
				if sid == "" {
					continue
				}
				sheetIDs = append(sheetIDs, sid)
			}
		}
		values := map[string]interface{}{}
		const batch = 10
		for i := 0; i < len(sheetIDs); i += batch {
			end := i + batch
			if end > len(sheetIDs) {
				end = len(sheetIDs)
			}
			q := larkcore.QueryParams{}
			for _, sid := range sheetIDs[i:end] {
				q.Add("ranges", sid)
			}
			vm, err := d.CollectObj(ctx, "sheets", "GET", "/open-apis/sheets/v2/spreadsheets/"+tok+"/values_batch_get", nil, q, SupUser|SupTenant)
			if err != nil {
				d.reqLog("sheet %s values: %v", tok, err)
				continue
			}
			if vrs, ok := vm["valueRanges"].([]interface{}); ok {
				for _, vr := range vrs {
					vrm, _ := vr.(map[string]interface{})
					rng := strVal(vrm["range"])
					sid := rng
					if ix := strings.Index(rng, "!"); ix >= 0 {
						sid = rng[:ix]
					}
					if sid != "" {
						values[sid] = vrm
					}
				}
			}
		}
		entry["values"] = values
		parts = append(parts, SplitPart{SubKey: tok, Object: entry})
	}
	return &SplitResult{SubField: "token", Path: "docs/sheets/{sub}.json", Parts: parts}, nil
}

func modBitable(ctx context.Context, d *Dumper) (interface{}, error) {
	idx := driveIndexIfAny(d, ctx)
	toks := unionStrings(idx.byType["bitable"], d.ids("bitable"))
	var parts []SplitPart
	for _, tok := range toks {
		if ctx.Err() != nil {
			break
		}
		appMeta, err := d.CollectObj(ctx, "bitable", "GET", "/open-apis/bitable/v1/apps/"+tok, nil, nil, SupUser|SupTenant)
		if err != nil {
			d.reqLog("bitable %s: %v", tok, err)
			continue
		}
		entry := map[string]interface{}{}
		entry["app"] = appMeta["app"]
		tables, err := d.Collect(ctx, ListConf{
			Name: "bitable_tables", Method: "GET", Path: "/open-apis/bitable/v1/apps/" + tok + "/tables",
			Supports: SupUser | SupTenant, PageSize: 100,
		})
		if err != nil {
			d.reqLog("bitable %s tables: %v", tok, err)
			continue
		}
		tableData := map[string]interface{}{}
		for _, tb := range tables {
			tm, _ := tb.(map[string]interface{})
			tid := strVal(tm["table_id"])
			if tid == "" {
				continue
			}
			te := map[string]interface{}{}
			if fields, err := d.Collect(ctx, ListConf{
				Name: "bitable_fields", Method: "GET", Path: "/open-apis/bitable/v1/apps/" + tok + "/tables/" + tid + "/fields",
				Supports: SupUser | SupTenant, PageSize: 100,
			}); err == nil {
				te["fields"] = fields
			} else {
				d.reqLog("bitable %s/%s fields: %v", tok, tid, err)
			}
			if views, err := d.Collect(ctx, ListConf{
				Name: "bitable_views", Method: "GET", Path: "/open-apis/bitable/v1/apps/" + tok + "/tables/" + tid + "/views",
				Supports: SupUser | SupTenant, PageSize: 100,
			}); err == nil {
				te["views"] = views
			} else {
				d.reqLog("bitable %s/%s views: %v", tok, tid, err)
			}
			if records, err := d.Collect(ctx, ListConf{
				Name: "bitable_records", Method: "GET", Path: "/open-apis/bitable/v1/apps/" + tok + "/tables/" + tid + "/records",
				Supports: SupUser | SupTenant, PageSize: 100,
			}); err == nil {
				te["records"] = records
				if !d.opt.NoDownload {
					var atts []map[string]string
					scanAttachments(records, &atts)
					for _, a := range atts {
						d.AddResource("bitable", a["file_token"], a["name"], map[string]interface{}{"file_token": a["file_token"], "table_id": tid, "app_token": tok})
					}
				}
			} else {
				d.reqLog("bitable %s/%s records: %v", tok, tid, err)
			}
			tableData[tid] = te
		}
		entry["tables"] = tableData
		entry["table_meta"] = tables
		parts = append(parts, SplitPart{SubKey: tok, Object: entry})
	}
	return &SplitResult{SubField: "token", Path: "docs/bitable/{sub}.json", Parts: parts}, nil
}

func modWiki(ctx context.Context, d *Dumper) (interface{}, error) {
	spaces, err := d.Collect(ctx, ListConf{
		Name: "wiki_spaces", Method: "GET", Path: "/open-apis/wiki/v2/spaces",
		Supports: SupUser | SupTenant, PageSize: 50,
	})
	if err != nil {
		d.log("wiki", "spaces list unavailable: "+err.Error())
		spaces = []interface{}{}
	}

	type nodePair struct{ spaceID, nodeToken, title, parent string }
	var parts []SplitPart
	var queue []nodePair
	for _, sp := range spaces {
		m, _ := sp.(map[string]interface{})
		sid := strVal(m["space_id"])
		if sid == "" {
			continue
		}
		sidKey := sid
		if nm := strVal(m["name"]); nm != "" {
			sidKey = sid + "_" + nm
		}
		// per-space index file
		info := map[string]interface{}{}
		if x, err := d.CollectObj(ctx, "wiki", "GET", "/open-apis/wiki/v2/spaces/"+sid, nil, nil, SupUser|SupTenant); err == nil {
			info = x
		} else {
			d.reqLog("wiki space %s: %v", sid, err)
		}
		info["space_id"] = sid
		parts = append(parts, SplitPart{SubKey: sidKey, Object: info, Items: []interface{}{}})
		queue = append(queue, nodePair{spaceID: sid, nodeToken: "", title: strVal(m["name"]), parent: ""})
	}

	visited := map[string]bool{}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		q := larkcore.QueryParams{}
		if p.nodeToken != "" {
			q.Set("parent_node_token", p.nodeToken)
		}
		items, err := d.Collect(ctx, ListConf{
			Name: "wiki_nodes", Method: "GET", Path: "/open-apis/wiki/v2/spaces/" + p.spaceID + "/nodes",
			Query: q, Supports: SupUser | SupTenant, PageSize: 50,
		})
		if err != nil {
			d.reqLog("wiki space %s nodes(%s): %v", p.spaceID, p.nodeToken, err)
			continue
		}
		for _, it := range items {
			m, _ := it.(map[string]interface{})
			ntok := strVal(m["node_token"])
			if ntok == "" {
				continue
			}
			nm := map[string]interface{}{}
			for k, v := range m {
				nm[k] = v
			}
			nm["space_id"] = p.spaceID
			nm["parent_node_token"] = p.nodeToken
			// node's payload is itself a docx/sheet/bitable: feed the content pools
			switch strVal(m["obj_type"]) {
			case "doc", "docx":
				d.addIDs("doc", strVal(m["obj_token"]))
			case "sheet":
				d.addIDs("sheet", strVal(m["obj_token"]))
			case "bitable":
				d.addIDs("bitable", strVal(m["obj_token"]))
			}
			// one json per document under <space>/
			rel := filepath.Join("docs", "wiki", sanitizeName(p.spaceID), sanitizeName(ntok)+".json")
			if err := d.out.WriteRaw(rel, map[string]interface{}{
				"group": "docs", "key": "wiki_spaces", "sub_field": "node_token",
				"sub_key": ntok, "generated_at": nowISO(), "object": nm,
			}); err != nil {
				return nil, err
			}
			if boolVal(m["has_child"]) && !visited[ntok] {
				visited[ntok] = true
				queue = append(queue, nodePair{spaceID: p.spaceID, nodeToken: ntok, title: strVal(m["title"]), parent: p.nodeToken})
			}
		}
	}

	// nodes harvested from message links (no space context)
	for _, tok := range d.ids("wiki") {
		q := larkcore.QueryParams{}
		q.Set("token", tok)
		n, err := d.CollectObj(ctx, "wiki", "GET", "/open-apis/wiki/v2/spaces/get_node", nil, q, SupUser|SupTenant)
		if err != nil {
			d.reqLog("wiki node %s: %v", tok, err)
			continue
		}
		rel := filepath.Join("docs", "wiki_pooled", sanitizeName(tok)+".json")
		if err := d.out.WriteRaw(rel, map[string]interface{}{
			"group": "docs", "key": "wiki_spaces", "sub_field": "token",
			"sub_key": tok, "generated_at": nowISO(), "object": n["node"],
		}); err != nil {
			return nil, err
		}
	}
	return &SplitResult{SubField: "space_id", Path: "docs/wiki/{sub}/space.json", Parts: parts}, nil
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, a...), b...) {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// nowISO returns the current RFC3339 timestamp.
func nowISO() string { return time.Now().Format(time.RFC3339) }
