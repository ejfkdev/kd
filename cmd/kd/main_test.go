package main

import (
	"testing"

	"github.com/ejfkdev/kd/internal/dingtalk"
	"github.com/ejfkdev/kd/internal/feishu"
)

func TestDetectCredential(t *testing.T) {
	cases := []struct {
		appID, token string
		feishu       bool
		dingtalk     bool
	}{
		// 飞书
		{"cli_aa8eacf9e1f91bc2", "", true, false},
		{"", "t-AbCdEfGhIjKlMnOp", true, false},
		{"", "u-abcdefghijklmn", true, false},
		// 钉钉
		{"dingmbw5n9ktkkbbjv3g", "", false, true},
		{"", "30609461091b3220a7fda07d25c5ca2e", false, true},
		// 无法识别
		{"", "", false, false},
		{"otherid", "", false, false},
	}
	for _, c := range cases {
		if got := feishu.DetectCredential(c.appID, c.token); got != c.feishu {
			t.Fatalf("feishu.DetectCredential(%q, %q) = %v, want %v", c.appID, c.token, got, c.feishu)
		}
		if got := dingtalk.DetectCredential(c.appID, c.token); got != c.dingtalk {
			t.Fatalf("dingtalk.DetectCredential(%q, %q) = %v, want %v", c.appID, c.token, got, c.dingtalk)
		}
	}
}

func TestRegistry(t *testing.T) {
	reg := registry()
	if len(reg) < 2 {
		t.Fatalf("registry has %d products", len(reg))
	}
	names := map[string]bool{}
	for _, s := range reg {
		if names[s.Name] {
			t.Fatalf("duplicate product %q", s.Name)
		}
		names[s.Name] = true
		if len(s.ListModules()) == 0 {
			t.Fatalf("%s has no modules", s.Name)
		}
	}
	if s, ok := specByName(reg, "lark"); !ok || s.Name != "feishu" {
		t.Fatalf("lark alias broken: %+v/%v", s, ok)
	}
}

func TestTranslateFlag(t *testing.T) {
	in := []string{"-token", "abc", "-app-id", "dingx"}
	out := translateFlag(in, "-token", "-access-token")
	if out[0] != "-access-token" || out[1] != "abc" || out[2] != "-app-id" {
		t.Fatalf("translate = %v", out)
	}
}

func TestPickFlag(t *testing.T) {
	argv := []string{"-app-id", "cli_x", "-app-secret", "s"}
	if got := pickFlag(argv, "-app-id", "-id", "-app-key"); got != "cli_x" {
		t.Fatalf("pick = %q", got)
	}
	if got := pickFlag(argv, "-id"); got != "" {
		t.Fatalf("pick empty = %q", got)
	}
}
