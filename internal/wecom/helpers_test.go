package wecom

import (
	"encoding/json"
	"testing"
)

func TestPickAny(t *testing.T) {
	var root map[string]interface{}
	json.Unmarshal([]byte(`{"info_list":[{"a":1}],"is_last":1,"next_cursor":"C2","n":3}`), &root)
	if l := pickPath(root, "info_list"); len(l) != 1 {
		t.Fatalf("list = %v", l)
	}
	if !truthyOf(pickAny(root, "is_last")) {
		t.Fatal("is_last=1 should be truthy")
	}
	if got := cursorStr(pickAny(root, "next_cursor")); got != "C2" {
		t.Fatalf("cursor = %q", got)
	}
	if got := cursorStr(pickAny(root, "n")); got != "3" {
		t.Fatalf("numeric cursor = %q", got)
	}
	if v := pickAny(root, "missing.deep"); v != nil {
		t.Fatalf("missing path should be nil: %v", v)
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
	if !retryableErr(&DumpError{Code: 45009, Msg: "接口调用超过限制"}) {
		t.Fatal("45009 must be retryable")
	}
	if !retryableErr(&DumpError{Code: -1, Msg: "系统繁忙"}) {
		t.Fatal("-1 must be retryable")
	}
	if retryableErr(&DumpError{Code: 60011, Msg: "无权限"}) {
		t.Fatal("60011 must NOT be retryable")
	}
	if retryableErr(&DumpError{Code: 40013, Msg: "invalid corpid"}) {
		t.Fatal("40013 must NOT be retryable")
	}
}

func TestDetectCredential(t *testing.T) {
	if !DetectCredential("ww1a2b3c4d5e6f7g8h", "") {
		t.Fatal("ww* corpid must be detected")
	}
	if !DetectCredential("wx1a2b3c4d5e6f7g8h", "") {
		t.Fatal("wx* corpid must be detected")
	}
	if DetectCredential("dingxxx", "") || DetectCredential("cli_xxx", "") || DetectCredential("", "") {
		t.Fatal("non-wecom appids must not be detected")
	}
}
