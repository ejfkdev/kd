package feishu

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestDumper() *Dumper {
	d := &Dumper{
		opt: &Options{QPS: 50, Retry: 1, DownloadThreads: 4},
	}
	d.limiter = newRateLimiter(d.opt.QPS)
	d.httpClient = &http.Client{Timeout: 30 * time.Second}
	d.budgets = map[string]*endpointBudget{}
	return d
}

// serveBig makes an httptest server serving n bytes of 'a'+i%26 with Range support.
func serveBig(t *testing.T, n int64) (*httptest.Server, []byte) {
	t.Helper()
	data := make([]byte, n)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	var gotRange atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			gotRange.Add(1)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "big.bin", time.Now(), io.NewSectionReader(
			readerAtBytes(data), 0, n))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []byte { return data }()
}

type readerAtBytes []byte

func (r readerAtBytes) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r)) {
		return 0, io.EOF
	}
	n := copy(p, r[off:])
	return n, nil
}

func TestDownloadToFileFresh(t *testing.T) {
	d := newTestDumper()
	srv, data := serveBig(t, 300_000)
	dir := t.TempDir()
	final := filepath.Join(dir, "r", "x.bin")
	size, resumed, _, err := d.downloadToFile(context.Background(),
		fetchTarget{url: srv.URL + "/x"}, final)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if resumed {
		t.Fatal("fresh download reported resumed=true")
	}
	if size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", size, len(data))
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatal("content mismatch")
	}
	if _, err := os.Stat(final + ".part"); !os.IsNotExist(err) {
		t.Fatal("part file should be renamed away")
	}
}

func TestDownloadToFileResume(t *testing.T) {
	d := newTestDumper()
	srv, data := serveBig(t, 300_000)
	dir := t.TempDir()
	final := filepath.Join(dir, "x.bin")
	part := final + ".part"
	// simulate a previous interrupted run: first 100KB present
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	half := data[:100_000]
	if err := os.WriteFile(part, half, 0o644); err != nil {
		t.Fatal(err)
	}
	size, resumed, _, err := d.downloadToFile(context.Background(),
		fetchTarget{url: srv.URL + "/x"}, final)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !resumed {
		t.Fatal("expected resumed=true")
	}
	if size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", size, len(data))
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatal("resumed content mismatch")
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal("part file should be renamed away after completion")
	}
}

// TestCancelLeavesPartForResume: interrupting a download must keep the .part
// file so the next run can resume from the bytes already written.
func TestCancelLeavesPartForResume(t *testing.T) {
	d := newTestDumper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", 1<<20))
		w.WriteHeader(http.StatusOK)
		chunk := make([]byte, 4096)
		for i := 0; i < 10; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		<-r.Context().Done() // hang until the client cancels
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	dir := t.TempDir()
	final := filepath.Join(dir, "x.bin")
	if _, _, _, err := d.downloadToFile(ctx, fetchTarget{url: srv.URL + "/x"}, final); err == nil {
		t.Fatal("expected the download to fail on cancel")
	}
	st, err := os.Stat(final + ".part")
	if err != nil {
		t.Fatalf("part file must remain for resume: %v", err)
	}
	if st.Size() == 0 {
		t.Fatal("part file is empty")
	}
	t.Logf("partial bytes kept: %d", st.Size())
}

func TestDownloadConcurrent(t *testing.T) {
	d := newTestDumper()
	dir := t.TempDir()
	var wgWait int
	_ = wgWait
	n := 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		srv, data := serveBig(t, 80_000)
		go func(i int, srv *httptest.Server, data []byte) {
			final := filepath.Join(dir, fmt.Sprintf("f%d.bin", i))
			_, _, _, err := d.downloadToFile(context.Background(),
				fetchTarget{url: srv.URL + "/f"}, final)
			if err != nil {
				errs <- err
				return
			}
			got, err := os.ReadFile(final)
			if err != nil {
				errs <- err
				return
			}
			if string(got) != string(data) {
				errs <- fmt.Errorf("f%d content mismatch", i)
				return
			}
			errs <- nil
		}(i, srv, data)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSniffImageExt(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// real image bytes via stdlib encoders
	var pngBuf, jpgBuf, gifBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpgBuf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBuf, image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black}), nil); err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"a.png":         pngBuf.Bytes(),
		"b.jpg":         jpgBuf.Bytes(),
		"c.gif":         gifBuf.Bytes(),
		"d-unknown.bin": []byte("NOTANIMAGEPAYLOAD00000000"),
	}
	for name, data := range cases {
		got := sniffImageExt(write(name, data))
		if filepath.Ext(name) == ".bin" {
			if got != "" {
				t.Fatalf("%s: sniff = %q, want empty", name, got)
			}
			continue
		}
		if !strings.EqualFold(filepath.Ext(name), got) {
			t.Fatalf("%s: sniff = %q, want %q", name, got, filepath.Ext(name))
		}
	}
}
