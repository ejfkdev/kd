package feishu

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Output lays the dump out as multiple independent JSONs under one directory:
//
//	<dir>/meta.json                       identity, scopes, stats
//	<dir>/<group>/<key>.json              one file per module, paged data merged
//	<dir>/resources/resources.json        resource records
//	<dir>/resources/<source>/<binary>     downloaded attachments/images
type Output struct {
	mu        sync.Mutex
	dir       string
	meta      map[string]interface{}
	metaOrder []string
	startedAt time.Time
}

func newOutput(dir string) *Output {
	return &Output{
		dir:       dir,
		meta:      map[string]interface{}{},
		startedAt: time.Now(),
	}
}

func (o *Output) Dir() string { return o.dir }

// ResourcesDir is where resource binaries and the resource index live.
func (o *Output) ResourcesDir() string { return filepath.Join(o.dir, "resources") }

func (o *Output) setMeta(key string, v interface{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.meta[key]; !ok {
		o.metaOrder = append(o.metaOrder, key)
	}
	o.meta[key] = v
}

// SplitResult asks WriteTask to write one JSON per sub-key instead of a single
// file. Path is a template containing {sub} (default "<group>/<key>/{sub}.json"),
// e.g. "im/chats/{sub}/detail.json" for per-chat layouts.
type SplitResult struct {
	SubField string
	Path     string
	Parts    []SplitPart
}

// SplitPart is one sub-file of a SplitResult; either Items (a list) or Object
// (a single object) is written.
type SplitPart struct {
	SubKey string
	Items  []interface{}
	Object map[string]interface{}
}

// WriteTask persists one module result as <dir>/<group>/<key>.json in a fixed
// envelope: arrays land in "items" (all pages merged), maps in "object".
func (o *Output) WriteTask(t task, v interface{}) error {
	if sr, ok := v.(*SplitResult); ok {
		return o.writeSplit(t, sr)
	}
	env := map[string]interface{}{
		"group":        t.Group,
		"key":          t.Key,
		"generated_at": time.Now().Format(time.RFC3339),
	}
	switch val := v.(type) {
	case []interface{}:
		items := val
		// strip the synthetic per-chat count element into "extra"
		if len(items) > 0 {
			if first, ok := items[0].(map[string]interface{}); ok {
				if _, is := first["_per_chat_counts"]; is {
					env["extra"] = first
					items = items[1:]
				}
			}
		}
		env["count"] = len(items)
		env["items"] = items
	case map[string]interface{}:
		if e, ok := val["error"].(string); ok && len(val) == 1 {
			env["error"] = e
			break
		}
		env["count"] = len(val)
		env["object"] = val
	default:
		env["object"] = v
	}
	name := t.Key
	if t.File != "" {
		name = t.File
	}
	rel := filepath.Join(t.Group, name+".json")
	if err := o.writeJSON(rel, env); err != nil {
		return fmt.Errorf("%s.%s: %w", t.Group, t.Key, err)
	}
	return nil
}

// writeSplit writes one file per non-empty part, honoring SplitResult.Path.
func (o *Output) writeSplit(t task, sr *SplitResult) error {
	for _, p := range sr.Parts {
		if len(p.Items) == 0 && len(p.Object) == 0 {
			continue
		}
		sub := sanitizeName(p.SubKey)
		if sub == "" {
			sub = "unknown"
		}
		rel := sr.Path
		if rel == "" {
			rel = filepath.Join(t.Group, t.Key, sub+".json")
		} else {
			rel = strings.ReplaceAll(rel, "{sub}", sub)
		}
		env := map[string]interface{}{
			"group":        t.Group,
			"key":          t.Key,
			"sub_field":    sr.SubField,
			"sub_key":      p.SubKey,
			"generated_at": time.Now().Format(time.RFC3339),
		}
		if len(p.Object) > 0 {
			env["object"] = p.Object
		} else {
			env["count"] = len(p.Items)
			env["items"] = p.Items
		}
		if err := o.writeJSON(rel, env); err != nil {
			return fmt.Errorf("%s.%s: %w", t.Group, t.Key, err)
		}
	}
	return nil
}

// WriteRaw persists an arbitrary envelope map at a relative path (used by
// modules with deeper custom layouts, e.g. wiki per-space trees).
func (o *Output) WriteRaw(rel string, env map[string]interface{}) error {
	return o.writeJSON(rel, env)
}

// TaskDone reports whether a previous run already produced this task's output
// (exact file for plain tasks; any <key>.json under Footprint for split tasks).
// Used by -resume for incremental reruns.
func (o *Output) TaskDone(t task) bool {
	name := t.Key
	if t.File != "" {
		name = t.File
	}
	if _, err := os.Stat(filepath.Join(o.dir, t.Group, name+".json")); err == nil {
		return true
	}
	if t.Footprint != "" {
		dir := filepath.Join(o.dir, t.Footprint)
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					if _, err := os.Stat(filepath.Join(dir, e.Name(), t.Key+".json")); err == nil {
						return true
					}
				}
			}
		}
	}
	return false
}

// WriteResources persists the resource index into resources/resources.json.
func (o *Output) WriteResources(records []ResourceRecord) error {
	env := map[string]interface{}{
		"group":        "resources",
		"key":          "resources",
		"count":        len(records),
		"generated_at": time.Now().Format(time.RFC3339),
		"items":        records,
	}
	return o.writeJSON(filepath.Join("resources", "resources.json"), env)
}

// WriteMeta writes meta.json (identity, granted scopes, stats, timing).
func (o *Output) WriteMeta(final bool) error {
	o.mu.Lock()
	meta := map[string]interface{}{}
	for _, k := range o.metaOrder {
		meta[k] = o.meta[k]
	}
	meta["elapsed_seconds"] = int(time.Since(o.startedAt).Seconds())
	o.mu.Unlock()

	root := map[string]interface{}{
		"tool":         "feishu-dump",
		"final":        final,
		"generated_at": time.Now().Format(time.RFC3339),
		"meta":         meta,
	}
	return o.writeJSON("meta.json", root)
}

// writeJSON writes one JSON file under the output dir atomically.
func (o *Output) writeJSON(rel string, v interface{}) error {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	abs := filepath.Join(o.dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, abs)
}

// Logf appends one timestamped line to run.log (request codes, errors,
// empty-result notices, rate-limit sleeps). Data JSONs stay clean.
func (o *Output) Logf(format string, args ...interface{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	abs := filepath.Join(o.dir, "run.log")
	if err := os.MkdirAll(o.dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	f.WriteString(line)
}

// LogPath returns the run.log path.
func (o *Output) LogPath() string { return filepath.Join(o.dir, "run.log") }
