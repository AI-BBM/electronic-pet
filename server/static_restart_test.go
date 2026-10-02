package server_test

// 生命周期与静态页用例（T14–T15）。

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// T14 服务重启（同一 dbPath 重新 New）后旧 token 仍有效。
func TestTokenSurvivesServerRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")

	h1 := newHandlerAt(t, dbPath)
	token := mustJoin(t, h1, "c1", "小明", "01")
	adopted := mustAdopt(t, h1, token, "random")

	// 模拟重启：在同一 DB 文件上重新构建 handler（签名密钥必须已持久化在 DB 中）。
	h2 := newHandlerAt(t, dbPath)

	resp, body := doJSON(t, h2, http.MethodGet, "/api/pet/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重启后旧 token 访问 /api/pet/me 状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
	}
	p := petFromAny(t, body["pet"])
	if p.Species.ID != adopted.Species.ID {
		t.Errorf("重启后宠物 species.id = %q, 与重启前 %q 不一致", p.Species.ID, adopted.Species.ID)
	}
}

// T15 静态页契约：GET / 返回 200 HTML，且包含 SPA 四视图标记。
func TestIndexHTMLContainsFourViews(t *testing.T) {
	h := newHandler(t)

	resp := performJSON(h, http.MethodGet, "/", "", nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 GET / 响应失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / 状态码 = %d, 期望 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, 期望包含 text/html", ct)
	}
	html := string(raw)
	for _, marker := range []string{
		`data-view="join"`,
		`data-view="eggs"`,
		`data-view="hatch"`,
		`data-view="pet"`,
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("index 页面缺少视图标记 %s", marker)
		}
	}
}
