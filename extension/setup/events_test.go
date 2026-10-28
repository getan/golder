package setup

import "testing"

func TestAuthorizationOriginBoundary(t *testing.T) {
	origins := []string{"https://accounts.feishu.cn"}
	if err := AuthorizationURL("https://accounts.feishu.cn/device?user_code=x", origins); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://accounts.feishu.cn/device", "file:///secret", "javascript:alert(1)", "https://accounts.feishu.cn.evil.example/device", "https://user@accounts.feishu.cn/device", "https://127.0.0.1/device", "https://localhost/device", "https://accounts.feishu.cn/device#fragment"} {
		if err := AuthorizationURL(raw, origins); err == nil {
			t.Fatalf("allowed %q", raw)
		}
	}
}
