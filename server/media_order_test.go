package server_test

// M5 hotfix 回归：@media (max-width: 1023px) 块必须位于 style.css 的所有
// 同选择器桌面规则之后。层叠语义：同特异性下源顺序后者胜，媒体查询不改变
// 特异性——若有人在媒体查询块之后追加 .dex-grid/.wall-item 等桌面规则，
// 窄屏覆盖会被静默吞掉（线上事故：dex 视图 375px 下 4 列撑出 676px 横滚）。
//
// 命令: go test ./server/ -run TestM5_MediaBlockMustBeLast -v

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestM5_MediaBlockMustBeLast(t *testing.T) {
	h := newHandler(t)
	resp := performJSON(h, http.MethodGet, "/style.css", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /style.css 状态码 = %d, 期望 200", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 style.css 失败: %v", err)
	}
	css := string(raw)

	first := strings.Index(css, "@media (max-width: 1023px)")
	if first < 0 {
		t.Fatal("style.css 缺少 @media (max-width: 1023px) 块（M5 契约被破坏）")
	}

	// 媒体查询块闭合之后：除空白与注释外不允许任何后续规则（否则层叠覆盖窄屏规则）
	tail := css[first:]
	depth := 0
	closed := false
	for i := 0; i < len(tail); i++ {
		switch tail[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				closed = true
				rest := stripCSSComments(strings.TrimSpace(tail[i+1:]))
				if rest != "" {
					t.Errorf("@media 块之后存在后续规则（层叠会覆盖窄屏规则）: %.120q", rest)
				}
			}
		}
	}
	if !closed {
		t.Fatal("style.css 的 @media 块未正确闭合")
	}
}

// stripCSSComments 去掉 CSS 注释（/* ... */，不嵌套）。
func stripCSSComments(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i:], "*/")
		if j < 0 {
			return b.String()
		}
		s = s[i+j+2:]
	}
}
