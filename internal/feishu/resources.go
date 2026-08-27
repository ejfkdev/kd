package feishu

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
)

// resourceItem is one discovered attachment/image/resource queued for download.
type resourceItem struct {
	Source string // business: im / drive / docx / bitable / calendar / task / minutes
	ID     string // original resource id (file key, token, attachment guid, ...)
	Name   string // original file name, may be empty
	Extra  map[string]interface{}
}

// ResourceRecord is what lands in resources/resources.json.
type ResourceRecord struct {
	Source  string                 `json:"source"`
	ID      string                 `json:"id"`
	Name    string                 `json:"name,omitempty"`
	Size    int64                  `json:"size,omitempty"`
	Path    string                 `json:"path,omitempty"` // relative to the output dir
	URL     string                 `json:"url,omitempty"`
	Resumed bool                   `json:"resumed,omitempty"`
	Error   string                 `json:"error,omitempty"`
	Extra   map[string]interface{} `json:"extra,omitempty"`
}

// AddResource queues a resource for the final download stage. Dedupes by
// source+id; if a later occurrence carries a name and the first had none,
// the name is upgraded.
func (d *Dumper) AddResource(source, id, name string, extra map[string]interface{}) {
	if id == "" {
		return
	}
	d.resMu.Lock()
	defer d.resMu.Unlock()
	key := source + "\x00" + id
	if ex, ok := d.resources[key]; ok {
		if ex.Name == "" && name != "" {
			ex.Name = name
		}
		return
	}
	d.resources[key] = &resourceItem{Source: source, ID: id, Name: name, Extra: extra}
	d.resOrder = append(d.resOrder, key)
}

// resourcePath returns the final on-disk location for one resource. When the
// original name is empty the file is "<来源>-<id>" (no trailing dash).
func resourcePath(resourcesDir string, it *resourceItem) string {
	return resourcePathNamed(resourcesDir, it, it.Name)
}

func resourcePathNamed(resourcesDir string, it *resourceItem, name string) string {
	dir := filepath.Join(resourcesDir, sanitizeName(it.Source))
	base := sanitizeName(it.Source) + "-" + sanitizeName(it.ID)
	if name = sanitizeName(name); name != "" {
		base += "-" + name
	}
	return filepath.Join(dir, base)
}

// resourcePathExt builds "<来源>-<id><ext>" for resources whose real name is
// unknown — the extension is sniffed from the downloaded bytes.
func resourcePathExt(resourcesDir string, it *resourceItem, ext string) string {
	dir := filepath.Join(resourcesDir, sanitizeName(it.Source))
	return filepath.Join(dir, sanitizeName(it.Source)+"-"+sanitizeName(it.ID)+ext)
}

// findExisting looks for an already-downloaded file of this resource under any
// known naming variant (named / unnamed / extension-suffixed).
func findExisting(resourcesDir string, it *resourceItem) (path string, size int64) {
	for _, p := range []string{
		resourcePath(resourcesDir, it),
	} {
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			return p, st.Size()
		}
	}
	// extension-suffixed variant (sniffed on a previous run)
	dir := filepath.Join(resourcesDir, sanitizeName(it.Source))
	prefix := sanitizeName(it.Source) + "-" + sanitizeName(it.ID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && (len(name) == len(prefix) || name[len(prefix)] == '.') {
			if info, err := e.Info(); err == nil && info.Size() > 0 {
				return filepath.Join(dir, name), info.Size()
			}
		}
	}
	return "", 0
}

// sniffImageExt detects the image type from the file header via the mature
// github.com/gabriel-vasile/mimetype library (pure-Go magic-number sniffing,
// supports png/jpg/gif/webp/bmp/tiff/... ). Returns the extension or "".
func sniffImageExt(path string) string {
	mtype, err := mimetype.DetectFile(path)
	if err != nil || mtype == nil || !strings.HasPrefix(mtype.String(), "image/") {
		return ""
	}
	return mtype.Extension()
}

// fetchTarget is one download attempt: either a Feishu API endpoint (carries
// auth + per-API rate budget) or a plain temporary URL.
type fetchTarget struct {
	api    bool
	url    string
	budget string // normalized API path for rate accounting (api targets only)
}

// downloadResources downloads every queued resource with N concurrent workers,
// resuming interrupted runs from ".part" files. No size limits.
func (d *Dumper) downloadResources(ctx context.Context) []ResourceRecord {
	d.resMu.Lock()
	items := make([]*resourceItem, 0, len(d.resOrder))
	for _, k := range d.resOrder {
		items = append(items, d.resources[k])
	}
	d.resMu.Unlock()

	threads := d.opt.DownloadThreads
	records := make([]ResourceRecord, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, threads)
	for i, it := range items {
		if ctx.Err() != nil {
			for j := i; j < len(items); j++ {
				records[j] = ResourceRecord{Source: items[j].Source, ID: items[j].ID, Name: items[j].Name, Error: "interrupted"}
			}
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, it *resourceItem) {
			defer wg.Done()
			defer func() { <-sem }()
			records[i] = d.downloadOne(ctx, it)
		}(i, it)
	}
	wg.Wait()
	d.log("resources", fmt.Sprintf("downloaded with %d threads, total %d", threads, len(items)))
	return records
}

// downloadOne downloads a single resource, trying its candidate targets in
// order until one succeeds.
func (d *Dumper) downloadOne(ctx context.Context, it *resourceItem) ResourceRecord {
	rec := ResourceRecord{
		Source: it.Source,
		ID:     it.ID,
		Name:   it.Name,
		URL:    strVal(it.Extra["url"]),
		Extra:  it.Extra,
	}
	final := resourcePath(d.out.ResourcesDir(), it)

	// already fully downloaded in a previous run (any naming variant)
	if p, sz := findExisting(d.out.ResourcesDir(), it); p != "" {
		rec.Size = sz
		rec.Path = filepath.ToSlash(strings.TrimPrefix(p, d.out.Dir()+string(filepath.Separator)))
		return rec
	}

	targets := resourceTargets(d, it)
	var lastErr error
	for _, t := range targets {
		if ctx.Err() != nil {
			rec.Error = "interrupted"
			return rec
		}
		size, resumed, cdName, err := d.downloadToFile(ctx, t, final)
		if err != nil {
			lastErr = err
			continue
		}
		if cdName != "" && sanitizeName(cdName) != sanitizeName(it.Name) {
			// the server revealed the real file name via Content-Disposition
			newFinal := resourcePathNamed(d.out.ResourcesDir(), it, cdName)
			if newFinal != final {
				if err := os.Rename(final, newFinal); err == nil {
					final = newFinal
				}
			}
			rec.Name = cdName
		} else if it.Name == "" {
			// no name anywhere: sniff the image type and append its extension
			// so the file is "<来源>-<id>.png" instead of "…-<id>-"
			if ext := sniffImageExt(final); ext != "" {
				newFinal := resourcePathExt(d.out.ResourcesDir(), it, ext)
				if newFinal != final {
					if err := os.Rename(final, newFinal); err == nil {
						final = newFinal
					}
				}
				if rec.Extra == nil {
					rec.Extra = map[string]interface{}{}
				}
				rec.Extra["ext"] = ext
			}
		}
		rec.Size = size
		rec.Resumed = resumed
		rec.Path = filepath.ToSlash(strings.TrimPrefix(final, d.out.Dir()+string(filepath.Separator)))
		return rec
	}
	if lastErr != nil {
		rec.Error = lastErr.Error()
	} else {
		rec.Error = "no download channel"
	}
	return rec
}

// resourceTargets builds the ordered download candidates for a resource.
func resourceTargets(d *Dumper, it *resourceItem) []fetchTarget {
	switch it.Source {
	case "im":
		mid := strVal(it.Extra["message_id"])
		q := ""
		if t := strVal(it.Extra["type"]); t != "" {
			q = "?type=" + t
		}
		var ts []fetchTarget
		if mid != "" {
			p := "/open-apis/im/v1/messages/" + mid + "/resources/" + it.ID + q
			ts = append(ts, fetchTarget{api: true, url: d.baseURL + p, budget: normalizePath(p)})
		}
		// /im/v1/files downloads by key alone; needed for files that appear in
		// merged-forwarded/shared messages (the message endpoint rejects those
		// with 234003 "File not in msg")
		p2 := "/open-apis/im/v1/files/" + it.ID
		ts = append(ts, fetchTarget{api: true, url: d.baseURL + p2, budget: normalizePath(p2)})
		return ts

	case "drive", "calendar", "bitable", "docx":
		tok := it.ID
		if ft := strVal(it.Extra["file_token"]); ft != "" {
			tok = ft
		}
		var ts []fetchTarget
		for _, p := range []string{
			"/open-apis/drive/v1/files/" + tok + "/download",
			"/open-apis/drive/v1/medias/" + tok + "/download",
		} {
			ts = append(ts, fetchTarget{api: true, url: d.baseURL + p, budget: normalizePath(p)})
		}
		return ts

	case "task":
		// drive file token first, then the 3-minute temporary url
		var ts []fetchTarget
		if ft := strVal(it.Extra["file_token"]); ft != "" {
			for _, p := range []string{
				"/open-apis/drive/v1/files/" + ft + "/download",
				"/open-apis/drive/v1/medias/" + ft + "/download",
			} {
				ts = append(ts, fetchTarget{api: true, url: d.baseURL + p, budget: normalizePath(p)})
			}
		}
		if u := strVal(it.Extra["url"]); u != "" {
			ts = append(ts, fetchTarget{url: u})
		}
		return ts

	case "minutes":
		if u := strVal(it.Extra["url"]); u != "" {
			return []fetchTarget{{url: u}}
		}
	}
	return nil
}

// downloadToFile streams one target to finalPath (part-file + rename), resuming
// from a leftover ".part" via the Range header when the server honors it.
// Returns the filename from Content-Disposition when the server supplies one.
func (d *Dumper) downloadToFile(ctx context.Context, t fetchTarget, final string) (int64, bool, string, error) {
	part := final + ".part"
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, false, "", err
	}

	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}
	resumed := offset > 0

	var lastErr error
	for attempt := 0; attempt <= d.opt.Retry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, resumed, "", ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
		if err != nil {
			return 0, resumed, "", err
		}
		if t.api {
			tok, err := d.downloadToken(ctx, attempt > 0)
			if err != nil {
				return 0, resumed, "", err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			d.waitBudget(ctx, t.budget)
			d.limiter.Wait(ctx)
		} else {
			d.limiter.Wait(ctx)
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		if t.api {
			d.reqCnt.Add(1)
		}
		resp, err := d.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if t.api {
			d.noteBudget(t.budget, resp.Header)
			d.reqLog("GET %s -> http %d", t.budget, resp.StatusCode)
		}

		switch resp.StatusCode {
		case http.StatusPartialContent: // resume from offset
		case http.StatusOK:
			offset = 0
			resumed = false
		case http.StatusRequestedRangeNotSatisfiable:
			resp.Body.Close()
			if offset > 0 {
				// part is already the full file: finish it off
				if err := os.Rename(part, final); err != nil {
					return 0, true, "", err
				}
				return offset, true, "", nil
			}
			return 0, false, "", fmt.Errorf("http 416 range not satisfiable")
		case http.StatusTooManyRequests:
			resp.Body.Close()
			lastErr = fmt.Errorf("http 429")
			d.sleepRetryAfter(ctx, resp.Header)
			continue
		case http.StatusUnauthorized, http.StatusForbidden:
			msg := respSnippet(resp)
			resp.Body.Close()
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, msg)
			if t.api && attempt < d.opt.Retry && d.internal {
				continue // attempt>0 forces a token refresh in downloadToken
			}
			return 0, resumed, "", lastErr
		default:
			msg := respSnippet(resp)
			resp.Body.Close()
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, msg)
			if resp.StatusCode >= 500 {
				continue
			}
			return 0, resumed, "", lastErr
		}

		cdName := contentDispositionName(resp.Header.Get("Content-Disposition"))
		flag := os.O_CREATE | os.O_WRONLY
		if offset > 0 {
			flag |= os.O_APPEND
		} else {
			flag |= os.O_TRUNC
		}
		f, err := os.OpenFile(part, flag, 0o644)
		if err != nil {
			resp.Body.Close()
			lastErr = err
			continue
		}
		written, copyErr := io.Copy(f, resp.Body)
		// on a broken stream, cut the partial tail chunk so a later resume
		// continues from clean bytes
		if copyErr != nil {
			f.Truncate(offset + written)
		}
		f.Sync()
		clErr := f.Close()
		resp.Body.Close()
		if copyErr != nil {
			// an error mid-append may have written garbage before dying (rare):
			// keep the part for resume; truncating already happened
			lastErr = copyErr
			if ctx.Err() != nil {
				return 0, true, "", ctx.Err()
			}
			continue
		}
		if clErr != nil {
			lastErr = clErr
			continue
		}
		if err := os.Rename(part, final); err != nil {
			return 0, resumed, "", err
		}
		return offset + written, resumed, cdName, nil
	}
	return 0, resumed, "", fmt.Errorf("giving up after %d retries: %w", d.opt.Retry, lastErr)
}

// contentDispositionName extracts a filename from a Content-Disposition header.
func contentDispositionName(h string) string {
	if h == "" {
		return ""
	}
	for _, p := range strings.Split(h, ";") {
		p = strings.TrimSpace(p)
		low := strings.ToLower(p)
		if strings.HasPrefix(low, "filename*=utf-8''") {
			if u, err := url.QueryUnescape(strings.Trim(p[len("filename*=utf-8''"):], `"`)); err == nil {
				return filepath.Base(u)
			}
			continue
		}
		if strings.HasPrefix(low, "filename=") {
			return filepath.Base(strings.Trim(strings.TrimSpace(p[len("filename="):]), `"`))
		}
	}
	return ""
}

// respSnippet reads a short, bounded body prefix for error messages.
func respSnippet(resp *http.Response) string {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// sanitizeName makes a file name safe for the local filesystem; empty input
// stays empty so the "<来源>-<id>-<原名>" scheme shows missing names as empty.
func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "..", "_")
	return strings.TrimSpace(name)
}

// scanImages walks nested JSON looking for image tokens (docx blocks carry
// {"image": {"token": ...}} on image blocks).
func scanImages(m map[string]interface{}, out *[]string) {
	if m == nil {
		return
	}
	if img, ok := m["image"]; ok {
		collect := func(one interface{}) {
			if im, ok2 := one.(map[string]interface{}); ok2 {
				if t := strVal(im["token"]); t != "" {
					*out = append(*out, t)
				}
			}
		}
		if arr, ok := img.([]interface{}); ok {
			for _, one := range arr {
				collect(one)
			}
		} else {
			collect(img)
		}
	}
	for _, v := range m {
		switch vv := v.(type) {
		case map[string]interface{}:
			scanImages(vv, out)
		case []interface{}:
			for _, one := range vv {
				if mm, ok := one.(map[string]interface{}); ok {
					scanImages(mm, out)
				}
			}
		}
	}
}

// scanAttachments walks nested JSON for attachment objects carrying a drive
// file_token (bitable record attachments, calendar event attachments).
func scanAttachments(v interface{}, out *[]map[string]string) {
	switch vv := v.(type) {
	case map[string]interface{}:
		if ft := strVal(vv["file_token"]); ft != "" {
			name := strVal(vv["name"])
			if name != "" || strVal(vv["url"]) != "" || strVal(vv["file_size"]) != "" {
				*out = append(*out, map[string]string{"file_token": ft, "name": name})
			}
		}
		for _, sub := range vv {
			scanAttachments(sub, out)
		}
	case []interface{}:
		for _, one := range vv {
			scanAttachments(one, out)
		}
	}
}
