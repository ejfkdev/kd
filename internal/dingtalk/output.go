package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Output mirrors the Feishu tool's on-disk convention: one JSON per module
// under <dir>/<group>/<key>.json, a meta.json, run.log and a resources/ tree.
type Output struct {
	mu        sync.Mutex
	dir       string
	meta      map[string]interface{}
	metaOrder []string
	startedAt time.Time
}

func newOutput(dir string) *Output {
	return &Output{dir: dir, meta: map[string]interface{}{}, startedAt: time.Now()}
}

func (o *Output) Dir() string          { return o.dir }
func (o *Output) ResourcesDir() string { return filepath.Join(o.dir, "resources") }

func (o *Output) SetMeta(key string, v interface{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.meta[key]; !ok {
		o.metaOrder = append(o.metaOrder, key)
	}
	o.meta[key] = v
}

// task describes one dump step; module ids look like "contact.users".
type task struct {
	Group string
	Key   string
	Desc  string
	Run   func(ctx context.Context, d *Dumper) (interface{}, error)
}

func (t task) id() string { return t.Group + "." + t.Key }

// WriteTask persists one module result with a fixed envelope.
func (o *Output) WriteTask(t task, v interface{}) error {
	env := map[string]interface{}{
		"group":        t.Group,
		"key":          t.Key,
		"generated_at": time.Now().Format(time.RFC3339),
	}
	switch val := v.(type) {
	case []interface{}:
		env["count"] = len(val)
		env["items"] = val
	case map[string]interface{}:
		env["count"] = len(val)
		env["object"] = val
	default:
		env["object"] = v
	}
	return o.writeJSON(filepath.Join(t.Group, t.Key+".json"), env)
}

// WriteResources persists the resource index into resources/resources.json.
func (o *Output) WriteResources(records []resourceRecord) error {
	env := map[string]interface{}{
		"group":        "resources",
		"key":          "resources",
		"count":        len(records),
		"generated_at": time.Now().Format(time.RFC3339),
		"items":        records,
	}
	return o.writeJSON(filepath.Join("resources", "resources.json"), env)
}

// WriteMeta writes meta.json.
func (o *Output) WriteMeta(final bool) error {
	o.mu.Lock()
	m := map[string]interface{}{}
	for _, k := range o.metaOrder {
		m[k] = o.meta[k]
	}
	m["elapsed_seconds"] = int(time.Since(o.startedAt).Seconds())
	o.mu.Unlock()
	return o.writeJSON("meta.json", map[string]interface{}{
		"tool":         "dingtalk-dump",
		"final":        final,
		"generated_at": time.Now().Format(time.RFC3339),
		"meta":         m,
	})
}

// Logf appends one timestamped line to run.log.
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
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

func (o *Output) LogPath() string { return filepath.Join(o.dir, "run.log") }

// writeJSON writes one indented JSON file under the output dir atomically.
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
