package httpjson

import (
	"testing"

	"example.com/project/engine/base/capability"
)

func TestPrepareMetaMintsTraceAndCountsHops(t *testing.T) {
	m, code := PrepareMeta(nil, 0)
	if code != "" || m[capability.MetaTrace] == "" || m[capability.MetaDepth] != "1" {
		t.Fatalf("%v %q", m, code)
	}
	m2, _ := PrepareMeta(map[string]string{capability.MetaTrace: "t1", capability.MetaDepth: "4", "k": "v"}, 0)
	if m2[capability.MetaTrace] != "t1" || m2[capability.MetaDepth] != "5" || m2["k"] != "v" {
		t.Fatalf("caller's trace id and own keys must survive: %v", m2)
	}
	if m3, _ := PrepareMeta(map[string]string{capability.MetaDepth: "junk"}, 0); m3[capability.MetaDepth] != "1" {
		t.Fatalf("junk depth counts as 0: %v", m3)
	}
}

func TestMaxDepthIsOffByDefaultAndRefusesWhenOn(t *testing.T) {
	if _, code := PrepareMeta(map[string]string{capability.MetaDepth: "1000"}, 0); code != "" {
		t.Fatal("unlimited by default")
	}
	if _, code := PrepareMeta(map[string]string{capability.MetaDepth: "3"}, 3); code != "depth_exceeded" {
		t.Fatalf("depth 3 with cap 3 must be refused, got %q", code)
	}
	if _, code := PrepareMeta(map[string]string{capability.MetaDepth: "2"}, 3); code != "" {
		t.Fatal("depth 2 with cap 3 is fine")
	}
}
