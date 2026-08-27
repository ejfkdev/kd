package feishu

import (
	"testing"
)

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/open-apis/im/v1/messages": "open-apis/im/v1/messages",
		"/open-apis/im/v1/chats/oc_52ebd3f2d12f547722e1e819a0679cb2/members":                            "open-apis/im/v1/chats/*/members",
		"/open-apis/im/v1/messages/om_x100b695959e8b8acb4ba62887a9d4bc/resources/file_v3_abc1234567890": "open-apis/im/v1/messages/*/resources/*",
		"/open-apis/contact/v3/users/ou_7192dcc88a39acf636a2d8a65242d21e":                               "open-apis/contact/v3/users/*",
		"/open-apis/drive/v1/files":                                      "open-apis/drive/v1/files",
		"/open-apis/drive/v1/files/HzYgdOa3wo96bbx3NH0cYtgXnxe/download": "open-apis/drive/v1/files/*/download",
	}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Fatalf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapHasNoData(t *testing.T) {
	if !mapHasNoData(map[string]interface{}{"a": []interface{}{}, "b": map[string]interface{}{}, "c": "", "d": nil}) {
		t.Fatal("expected all-empty map to be data-less")
	}
	if mapHasNoData(map[string]interface{}{"a": []interface{}{}, "b": []interface{}{1}}) {
		t.Fatal("non-empty list must count as data")
	}
	if mapHasNoData(map[string]interface{}{"a": ""}) == false {
		t.Fatal("single empty string must count as no data")
	}
	if mapHasNoData(map[string]interface{}{"a": "x"}) {
		t.Fatal("non-empty string must count as data")
	}
}

func TestFileContentKeyName(t *testing.T) {
	key, name := fileContentKeyName(`{"file_key":"file_v3_abc","file_name":"报告.pdf"}`)
	if key != "file_v3_abc" || name != "报告.pdf" {
		t.Fatalf("json content parse = %q/%q", key, name)
	}
	key2, name2 := fileContentKeyName("file_v3_plain")
	if key2 != "file_v3_plain" || name2 != "" {
		t.Fatalf("plain content parse = %q/%q", key2, name2)
	}
	key3, name3 := fileContentKeyName(`{"image_key":"img_v3_xyz"}`)
	if key3 != "img_v3_xyz" || name3 != "" {
		t.Fatalf("image content parse = %q/%q", key3, name3)
	}
}

func TestPostResources(t *testing.T) {
	content := `{"post":{"zh_cn":{"title":"t","content":[[{"tag":"img","image_key":"img_a"},{"tag":"text","text":"hi"},{"tag":"file","file_key":"file_b","file_name":"x.zip"}]]}}}`
	var got []string
	postResources(content, func(k, typ, name string) {
		got = append(got, typ+"|"+k+"|"+name)
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 elements, got %v", got)
	}
	if got[0] != "image|img_a|" || got[1] != "file|file_b|x.zip" {
		t.Fatalf("unexpected elements: %v", got)
	}
}

func TestContentDispositionName(t *testing.T) {
	if got := contentDispositionName(`attachment; filename="报告 (1).pdf"`); got != "报告 (1).pdf" {
		t.Fatalf("filename= parse: %q", got)
	}
	if got := contentDispositionName(`attachment; filename*=UTF-8''%E6%8A%A5%E5%91%8A.pdf`); got != "报告.pdf" {
		t.Fatalf("filename* parse: %q", got)
	}
	if got := contentDispositionName(""); got != "" {
		t.Fatalf("empty header: %q", got)
	}
}

func TestResourcePathNaming(t *testing.T) {
	it := &resourceItem{Source: "im", ID: "img_a", Name: ""}
	it2 := &resourceItem{Source: "im", ID: "file_b", Name: "档案 .xlsx"}
	if got := resourcePath("/tmp/d", it); got != "/tmp/d/im/im-img_a" {
		t.Fatalf("empty name must have no trailing dash: %q", got)
	}
	if got := resourcePathNamed("/tmp/d", it2, it2.Name); got != "/tmp/d/im/im-file_b-档案 .xlsx" {
		t.Fatalf("named resource path: %q", got)
	}
	if got := resourcePathExt("/tmp/d", it, ".png"); got != "/tmp/d/im/im-img_a.png" {
		t.Fatalf("ext path: %q", got)
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName(""); got != "" {
		t.Fatalf("empty stays empty, got %q", got)
	}
	if got := sanitizeName("a/..\\\\b"); got != "a____b" {
		t.Fatalf("sanitize = %q", got)
	}
}
