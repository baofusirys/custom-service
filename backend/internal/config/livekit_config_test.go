package config

import (
	"strings"
	"testing"
)

const goodSecret = "Xk2pQ9vR7mN4sT1wY8zB5cD3fG6hJ0kLqWeRtYuI"

func TestNormalizeLiveKitDisabledWhenKeyEmpty(t *testing.T) {
	lk := LiveKitConfig{APISecret: "anything"}
	if err := normalizeLiveKit(&lk, "cs.example.com"); err != nil {
		t.Fatalf("未配置 key 时不应报错：%v", err)
	}
	if lk.Enabled() || lk.APISecret != "" {
		t.Fatal("未配置 key 时大厅必须关闭且不保留 secret")
	}
}

func TestNormalizeLiveKitDefaultsAndIDN(t *testing.T) {
	lk := LiveKitConfig{APIKey: "APIh4llK3y", APISecret: goodSecret}
	if err := normalizeLiveKit(&lk, "k7m2x9p3.卖号.com"); err != nil {
		t.Fatal(err)
	}
	if lk.InternalURL != "http://cs-livekit:7880" {
		t.Fatalf("内网地址默认值错误：%s", lk.InternalURL)
	}
	if lk.PublicURL != "wss://k7m2x9p3.xn--5kroo.com/lk" {
		t.Fatalf("公网地址应默认 wss://<域名>/lk 且转 punycode：%s", lk.PublicURL)
	}
	lk2 := LiveKitConfig{APIKey: "APIh4llK3y", APISecret: goodSecret, InternalURL: "http://10.0.0.5:7880/", PublicURL: "wss://cs.example.com:8443/lk/"}
	if err := normalizeLiveKit(&lk2, ""); err != nil {
		t.Fatal(err)
	}
	if lk2.InternalURL != "http://10.0.0.5:7880" || lk2.PublicURL != "wss://cs.example.com:8443/lk" {
		t.Fatalf("规范化错误：%+v", lk2)
	}
}

func TestNormalizeLiveKitRejectsWeakOrBadValues(t *testing.T) {
	cases := []struct {
		name string
		lk   LiveKitConfig
		dom  string
		want string
	}{
		{"短密钥", LiveKitConfig{APIKey: "APIh4llK3y", APISecret: "short"}, "a.com", "LIVEKIT_API_SECRET"},
		{"占位密钥", LiveKitConfig{APIKey: "APIh4llK3y", APISecret: "changeme-changeme-changeme-changeme-12"}, "a.com", "LIVEKIT_API_SECRET"},
		{"含 secret 的密钥", LiveKitConfig{APIKey: "APIh4llK3y", APISecret: "my-super-secret-value-0123456789abcdef"}, "a.com", "LIVEKIT_API_SECRET"},
		{"示例 key", LiveKitConfig{APIKey: "devkey", APISecret: goodSecret}, "a.com", "LIVEKIT_API_KEY"},
		{"key 含冒号", LiveKitConfig{APIKey: "API:x", APISecret: goodSecret}, "a.com", "LIVEKIT_API_KEY"},
		{"key 含空格", LiveKitConfig{APIKey: "API x", APISecret: goodSecret}, "a.com", "LIVEKIT_API_KEY"},
		{"内网地址带路径", LiveKitConfig{APIKey: "APIh4llK3y", APISecret: goodSecret, InternalURL: "http://cs-livekit:7880/twirp"}, "a.com", "LIVEKIT_INTERNAL_URL"},
		{"内网地址协议错", LiveKitConfig{APIKey: "APIh4llK3y", APISecret: goodSecret, InternalURL: "ftp://x"}, "a.com", "LIVEKIT_INTERNAL_URL"},
		{"公网地址协议错", LiveKitConfig{APIKey: "APIh4llK3y", APISecret: goodSecret, PublicURL: "https://a.com/lk"}, "a.com", "LIVEKIT_PUBLIC_URL"},
		{"无域名无公网地址", LiveKitConfig{APIKey: "APIh4llK3y", APISecret: goodSecret}, "", "LIVEKIT_PUBLIC_URL"},
	}
	for _, tc := range cases {
		lk := tc.lk
		err := normalizeLiveKit(&lk, tc.dom)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s：期望含 %s 的错误，实际 %v", tc.name, tc.want, err)
		}
		if strings.Contains(err.Error(), tc.lk.APISecret) && tc.lk.APISecret != "" {
			t.Fatalf("%s：错误信息不得包含密钥值", tc.name)
		}
	}
}
