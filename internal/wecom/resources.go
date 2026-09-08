package wecom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
)

// resourceItem is one discovered downloadable resource.
type resourceItem struct {
	Source string
	ID     string
	Name   string
	Extra  map[string]interface{}
}

// resourceRecord lands in resources/resources.json.
type resourceRecord struct {
	Source  string                 `json:"source"`
	ID      string                 `json:"id"`
	Name    string                 `json:"name,omitempty"`
	Size    int64                  `json:"size,omitempty"`
	Path    string                 `json:"path,omitempty"`
	URL     string                 `json:"url,omitempty"`
	Resumed bool                   `json:"resumed,omitempty"`
	Error   string                 `json:"error,omitempty"`
	Extra   map[string]interface{} `json:"extra,omitempty"`
}

// AddResource queues a resource (deduped by source+id).
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

// DownloadResources processes the queue with N workers; no size limits;
// interrupted files resume from ".part" where the server honors Range.
func (d *Dumper) DownloadResources(ctx context.Context) []resourceRecord {
	d.resMu.Lock()
	items := make([]*resourceItem, 0, len(d.resOrder))
	for _, k := range d.resOrder {
		items = append(items, d.resources[k])
	}
	d.resMu.Unlock()

	threads := d.opt.Workers
	records := make([]resourceRecord, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, threads)
	for i, it := range items {
		if ctx.Err() != nil {
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
	return records
}

func (d *Dumper) downloadOne(ctx context.Context, it *resourceItem) resourceRecord {
	rec := resourceRecord{Source: it.Source, ID: it.ID, Name: it.Name, URL: strVal(it.Extra["url"]), Extra: it.Extra}
	final := filepath.Join(d.out.ResourcesDir(), sanitizeName(it.Source), sanitizeName(it.Source)+"-"+sanitizeName(it.ID))
	if name := sanitizeName(it.Name); name != "" {
		final += "-" + name
	}
	if p, sz := findExisting(final); p != "" {
		rec.Size = sz
		rec.Path = filepath.ToSlash(strings.TrimPrefix(p, d.out.Dir()+string(filepath.Separator)))
		return rec
	}

	var lastErr error
	// 1) WeCom media download (media_id) — 素材/临时媒体文件
	if mid := strVal(it.Extra["media_id"]); mid != "" {
		if data, err := d.do(ctx, callSpec{method: "GET", path: "/media/get", query: map[string]string{"media_id": mid}, raw: true}); err == nil {
			name := strVal(it.Extra["name"])
			if n, err := saveFile(final, name, data); err == nil {
				n = sniffAndRename(n)
				rec.Size, _ = fileSize(n)
				rec.Path = filepath.ToSlash(strings.TrimPrefix(n, d.out.Dir()+string(filepath.Separator)))
				return rec
			}
		} else {
			lastErr = err
		}
	}
	// 2) plain URL (logo/avatar 等公网或带签名地址)
	if u := strVal(it.Extra["url"]); u != "" {
		n, err := d.downloadURL(ctx, u, final)
		if err != nil {
			lastErr = err
		} else {
			n = sniffAndRename(n)
			rec.Size, _ = fileSize(n)
			if nm := strVal(it.Extra["name"]); nm != "" {
				rec.Name = nm
			}
			if ext := filepath.Ext(filepath.Base(n)); ext != "" && rec.Name == "" {
				rec.Extra["ext"] = ext
			}
			rec.Path = filepath.ToSlash(strings.TrimPrefix(n, d.out.Dir()+string(filepath.Separator)))
			return rec
		}
	}
	if lastErr != nil {
		rec.Error = lastErr.Error()
	} else {
		rec.Error = "no download channel"
	}
	return rec
}

// downloadURL streams a plain URL to final with .part resume. If the final
// name changes after sniffing (extension appended), rename and return path.
func (d *Dumper) downloadURL(ctx context.Context, u, final string) (string, error) {
	part := final + ".part"
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		return "", err
	}
	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}
	for attempt := 0; attempt <= d.opt.Retry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
			}
		}
		d.limiter.Wait(ctx)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		if d.proxy != nil {
			client.Transport = &http.Transport{Proxy: http.ProxyURL(d.proxy)}
		}
		resp, err := client.Do(req)
		if err != nil {
			if strings.Contains(err.Error(), "timeout") {
				continue
			}
			return "", err
		}
		switch resp.StatusCode {
		case http.StatusPartialContent:
		case http.StatusOK:
			offset = 0
		case http.StatusRequestedRangeNotSatisfiable:
			resp.Body.Close()
			os.Rename(part, final)
			return final, nil
		default:
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests && attempt < d.opt.Retry {
				continue
			}
			return "", fmt.Errorf("http %d: %.200s", resp.StatusCode, string(msg))
		}
		flag := os.O_CREATE | os.O_WRONLY
		if offset > 0 {
			flag |= os.O_APPEND
		} else {
			flag |= os.O_TRUNC
		}
		f, err := os.OpenFile(part, flag, 0o644)
		if err != nil {
			resp.Body.Close()
			return "", err
		}
		_, copyErr := io.Copy(f, resp.Body)
		f.Sync()
		f.Close()
		resp.Body.Close()
		if copyErr != nil {
			return "", copyErr
		}
		os.Rename(part, final)
		break
	}
	return final, nil
}

// sniffAndRename appends the detected image extension to an unnamed resource
// file and returns the final path (unchanged when not an image).
func sniffAndRename(path string) string {
	mtype, err := mimetype.DetectFile(path)
	if err != nil || mtype == nil || !strings.HasPrefix(mtype.String(), "image/") {
		return path
	}
	np := path + mtype.Extension()
	if err := os.Rename(path, np); err == nil {
		return np
	}
	return path
}

func findExisting(final string) (string, int64) {
	if st, err := os.Stat(final); err == nil && st.Size() > 0 {
		return final, st.Size()
	}
	return "", 0
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func saveFile(final, name string, data []byte) (string, error) {
	if name != "" {
		final += "-" + sanitizeName(name)
	}
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(final+".tmp", data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(final+".tmp", final); err != nil {
		return "", err
	}
	return final, nil
}

// sanitizeName mirrors the other tools' naming hygiene.
func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "..", "_")
	return strings.TrimSpace(name)
}
