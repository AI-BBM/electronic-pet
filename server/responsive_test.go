package server_test

// Issue #23 测试先行（M5 移动端适配，纯展示层，API 零改动）。
// 契约来源 docs/product/features/m5-mobile.md，静态字符串级黑盒断言：
// - TestM5ViewportMetaAllPages：/、/log.html、/teacher.html 三页 viewport
//   均为 width=device-width, initial-scale=1，width=1280 必须消失。
// - TestM5MediaQueriesPresent：style.css 存在 @media (max-width: 1023px)
//   窄屏块（≥1024px 保持桌面布局）；触屏口径 min-height: 44px 与
//   font-size: 16px（防 iOS 聚焦缩放）；log.html 有窄屏块且 .dev-token input
//   的固定 420px 改为自适应（width:100% / max-width:100% 覆盖）；
//   teacher.html 有窄屏块且花名册卡片化机制存在（td 模板 data-label 属性 +
//   content:attr(data-label) 或等价规则）。
// - TestM5DesktopViewsRegression：桌面回归——index.html 六个 data-view
//   标记一个不少，style.css 的 .dex-grid 桌面布局键仍在。
//
// 预期红态：实现落地前三页 viewport 未改（index 仍 width=1280）、
// style.css 无 @media、teacher.html 无 data-label → 前两个用例 FAIL 属预期；
// 桌面回归用例在落地前后均应 PASS（桌面布局本就未动）。
// 黑盒口径：只经 newHandler 构建的 handler 发真实 GET，断言响应体字符串，
// 不感知文件位置与内部实现（注意 /log.html 必须由 handler 真正直出
// log.html 内容，而非 SPA 回退的 index.html）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// m5Pages 是 M5 移动端适配覆盖的三个页面路径。
var m5Pages = []string{"/", "/log.html", "/teacher.html"}

// m5Views 是 index.html SPA 必须保留的六个视图标记（桌面回归口径）。
var m5Views = []string{"join", "eggs", "hatch", "pet", "dex", "wall"}

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

// TestM5ViewportMetaAllPages 校验三个页面 viewport 移动适配契约：
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
// b) log.html：页内 @media 窄屏块，且固定 420px 有 width:100%/max-width:100%
//    自适应覆盖（字符串级粗检）；
// c) teacher.html：页内 @media 窄屏块 + 花名册卡片化机制——td 渲染模板带
//    data-label 属性，且存在 attr(data-label) 取标签内容的 CSS 规则
//    （teacher.html 内联或 style.css 任一出现即算）。
func TestM5MediaQueriesPresent(t *testing.T) {
	h := newHandler(t)

	css := m5Get(t, h, "/style.css")
	m5Want(t, "/style.css", css, `@media (max-width: 1023px)`, true)
	m5Want(t, "/style.css", css, `min-height: 44px`, true)
	m5Want(t, "/style.css", css, `font-size: 16px`, true)

	logBody := m5Get(t, h, "/log.html")
	m5Want(t, "/log.html", logBody, `@media`, true)
	if !m5Contains(logBody, `width:100%`) && !m5Contains(logBody, `max-width:100%`) {
		t.Errorf("GET /log.html: 窄屏块未见 .dev-token input 固定 420px 的自适应覆盖（width:100%% 与 max-width:100%% 均缺失）")
	}

	teacherBody := m5Get(t, h, "/teacher.html")
	m5Want(t, "/teacher.html", teacherBody, `@media`, true)
	m5Want(t, "/teacher.html", teacherBody, `data-label`, true)
	if !m5Contains(teacherBody, `attr(data-label)`) && !m5Contains(css, `attr(data-label)`) {
		t.Errorf("teacher.html: 未见 td::before 或等价的 content:attr(data-label) 卡片化 CSS 规则（teacher.html 内联与 style.css 均缺失）")
	}
}

// TestM5DesktopViewsRegression 桌面回归：移动适配不得破坏桌面骨架——
// index.html 六个 data-view 视图标记一个不少；style.css 的 .dex-grid
// 桌面布局键仍在（≥1024px 由媒体查询保护，规则本体不得删除）。
func TestM5DesktopViewsRegression(t *testing.T) {
	h := newHandler(t)

	idx := m5Get(t, h, "/")
	for _, view := range m5Views {
		m5Want(t, "/", idx, `data-view="`+view+`"`, true)
	}

	css := m5Get(t, h, "/style.css")
	m5Want(t, "/style.css", css, `.dex-grid`, true)
}
