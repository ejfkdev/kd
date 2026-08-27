package dingtalk

import (
	"encoding/json"
	"testing"
)

func TestPickPath(t *testing.T) {
	var root map[string]interface{}
	json.Unmarshal([]byte(`{"result":{"list":[{"userid":"a"},{"userid":"b"}],"next_cursor":10,"has_more":true}}`), &root)
	if l := pickPath(root, "result.list"); len(l) != 2 {
		t.Fatalf("list = %v", l)
	}
	if !pickBool(root, "result.has_more") {
		t.Fatal("has_more should be true")
	}
	if n, ok := pickNum(root, "result.next_cursor"); !ok || n != 10 {
		t.Fatalf("cursor = %d/%v", n, ok)
	}
	if l := pickPath(root, "result.missing"); l != nil {
		t.Fatalf("missing path should be nil: %v", l)
	}
}

func TestSplitDots(t *testing.T) {
	got := splitDots("result.list")
	if len(got) != 2 || got[0] != "result" || got[1] != "list" {
		t.Fatalf("split = %v", got)
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName(""); got != "" {
		t.Fatalf("empty stays empty: %q", got)
	}
	if got := sanitizeName("a/..\\b"); got != "a___b" {
		t.Fatalf("sanitize = %q", got)
	}
}

func TestDumpErrorRetryable(t *testing.T) {
	if !retryableErr(&DumpError{Code: 90018, Msg: "触发限流"}) {
		t.Fatal("90018 must be retryable")
	}
	if retryableErr(&DumpError{Code: 40014, Msg: "token invalid"}) {
		t.Fatal("40014 must NOT be retryable")
	}
}
