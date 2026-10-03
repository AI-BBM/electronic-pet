package server_test

// Issue #23 测试（M5 移动端适配，纯展示层）——M6（#25）处置后收窄版。
// M6 下线学生侧 SPA 与 log.html，本文件保留仍成立的展示层契约：
// - TestM5ViewportMetaAllPages：/ 与 /teacher.html viewport 均为
//   width=device-width, initial-scale=1（M6 后 / 返回教师 SPA）。
// - TestM5MediaQueriesPresent：style.css 窄屏块与触屏口径；teacher.html
//   页内窄屏块与花名册卡片化机制（td data-label + attr(data-label) 规则）。
// 桌面回归与教师页内容契约由 M6-T17 覆盖；style.css 媒体块层叠顺序由
// TestM5_MediaBlockMustBeLast 守护。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// m5Pages 是 M5 移动端适配覆盖的页面路径（M6 处置后：/ 与 /teacher.html）。
var m5Pages = []string{"/", "/teacher.html"}

// m5Get 经 handler 发真实 GET，断言 200 并返回响应体全文。
func m5Get(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s 状态码 = %d, 期望 200", target, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 GET %s 响应体失败: %v", target, err)
	}
	return string(raw)
}

// m5Contains 做空白不敏感的子串匹配：把 body 与 needle 的全部空白字符剔除后再
// Contains，兼容 `min-height: 44px` / `min-height:44px`、
// `@media (max-width: 1023px)` / `@media(max-width:1023px)` 等等价书写。
func m5Contains(body, needle string) bool {
	strip := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\r', '\n':
				return -1
			}
			return r
		}, s)
	}
	return strings.Contains(strip(body), strip(needle))
}

// m5Want 通用断言：body 含（或不含）指定字符串，失败时报页面上下文。
func m5Want(t *testing.T, target, body, needle string, wantContains bool) {
	t.Helper()
	got := m5Contains(body, needle)
	switch {
	case wantContains && !got:
		t.Errorf("GET %s: 响应体缺少 %q", target, needle)
	case !wantContains && got:
		t.Errorf("GET %s: 响应体不应再包含 %q", target, needle)
	}
}

// TestM5ViewportMetaAllPages 校验页面 viewport 移动适配契约：
// 200 直出；content 同时含 width=device-width 与 initial-scale=1；
// 桌面期遗留的 width=1280 必须消失。
func TestM5ViewportMetaAllPages(t *testing.T) {
	h := newHandler(t)
	for _, page := range m5Pages {
		t.Run("GET "+page, func(t *testing.T) {
			body := m5Get(t, h, page) // 隐含断言 200
			m5Want(t, page, body, `width=device-width`, true)
			m5Want(t, page, body, `initial-scale=1`, true)
			m5Want(t, page, body, `width=1280`, false)
		})
	}
}

// TestM5MediaQueriesPresent 校验窄屏媒体查询与触屏口径的存在性：
// a) style.css：@media (max-width: 1023px) 窄屏块 + 可点目标 min-height: 44px
//    + 表单 font-size: 16px（防 iOS 聚焦缩放）；
// b) teacher.html：页内 @media 窄屏块 + 花名册卡片化机制——td 渲染模板带
//    data-label 属性，且存在 attr(data-label) 取标签内容的 CSS 规则
//    （teacher.html 内联或 style.css 任一出现即算）。
func TestM5MediaQueriesPresent(t *testing.T) {
	h := newHandler(t)

	css := m5Get(t, h, "/style.css")
	m5Want(t, "/style.css", css, `@media (max-width: 1023px)`, true)
	m5Want(t, "/style.css", css, `min-height: 44px`, true)
	m5Want(t, "/style.css", css, `font-size: 16px`, true)

	teacherBody := m5Get(t, h, "/teacher.html")
	m5Want(t, "/teacher.html", teacherBody, `@media`, true)
	m5Want(t, "/teacher.html", teacherBody, `data-label`, true)
	if !m5Contains(teacherBody, `attr(data-label)`) && !m5Contains(css, `attr(data-label)`) {
		t.Errorf("teacher.html: 未见 td::before 或等价的 content:attr(data-label) 卡片化 CSS 规则（teacher.html 内联与 style.css 均缺失）")
	}
}
