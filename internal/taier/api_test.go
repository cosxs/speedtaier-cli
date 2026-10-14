package taier

import "testing"

func TestServerListEntry(t *testing.T) {
	manifest := []configEntry{
		{Name: "setting", Filename: "settings_v3_default.xml"},
		{Name: "serverlist_encrypt_url", Filename: "https://down.example/serverlist_encrypt.json", MD5: "abc"},
		{Name: "videores_url", Filename: "https://down.example/video_rules.dat"}, // 排在后面的其他 http 地址不能覆盖
	}
	e, ok := serverListEntry(manifest)
	if !ok || e.Filename != "https://down.example/serverlist_encrypt.json" || e.MD5 != "abc" {
		t.Errorf("serverListEntry = %+v, %v", e, ok)
	}
	if _, ok := serverListEntry(manifest[:1]); ok {
		t.Error("serverListEntry without entry: want not found")
	}
}
