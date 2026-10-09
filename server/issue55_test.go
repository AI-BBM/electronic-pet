package server_test

// Issue #55 F1 前端联调（M12 皮肤/积分 UI 落地 teacher.html）测试先行用例 T1–T10。
//
// 现状：M12 后端（皮肤目录/购买/切换 + roster 的 currency/activeScene）已合 main
// （PR #52），web/static/teacher.html 尚无任何皮肤/积分余额 UI。本文件钉死前端契约：
//   - T1–T6 内容契约：对 GET /teacher.html 响应体做源码级断言（风格仿 m6_test /
//     responsive_test 的 m5Get/m5Contains 空白不敏感匹配）；
//   - T7–T8 无回归 + 行为结构：既有锚点（对照 origin/main:web/static/teacher.html
//     提取的清单）不得缺失/改名，IIFE 与核心函数仍在，文件 UTF-8 可解码；
//   - T9–T10 对齐类：真实 httptest 流程证明 roster→catalog 字段对齐，且前端
//     fetch 路径与 server.go 的 mux 注册一致。
//
// 红态口径（实现未写时）：各用例的失败必须是「断言前端尚未实现的内容/行为」的
// Errorf/Fatalf，不得是编译错误或 panic。T7 的守护性子断言（既有锚点）红态本就
// 通过——它们只能在实现阶段改坏锚点时失败；T7 与 T8 合并为一个用例，红态失败
// 由 T8 的新增结构要求（皮肤商店加载函数）承担。后端皮肤逻辑本身由
// issue51_test.go 覆盖，本文件不重复测后端。

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// ---------- 本文件私有 helper（前缀 m55，避免与包内既有 helper 冲突） ----------

// m55Page 拉取教师页全文（经 handler 发真实 GET /teacher.html，隐含断言 200）。
func m55Page(t *testing.T, h http.Handler) string {
	t.Helper()
	return m5Get(t, h, "/teacher.html")
}

// m55Count 统计子串出现次数（原文匹配）。
func m55Count(body, needle string) int {
	return strings.Count(body, needle)
}

// m55Around 取 center 首次出现位置前 before、后 after 字符的窗口；
// center 不存在时返回 ok=false（调用方按「内容缺失」报红）。
func m55Around(body, center string, before, after int) (string, bool) {
	i := strings.Index(body, center)
	if i < 0 {
		return "", false
	}
	lo := i - before
	if lo < 0 {
		lo = 0
	}
	hi := i + after
	if hi > len(body) {
		hi = len(body)
	}
	return body[lo:hi], true
}

// m55AnyContains 任一 needle 命中（空白不敏感）即 true。
func m55AnyContains(body string, needles ...string) bool {
	for _, n := range needles {
		if m5Contains(body, n) {
			return true
		}
	}
	return false
}

// m55FnRegion 取从 start（"function xxx" 声明处）到下一个 6 空格缩进的顶层
// function 声明（或文末）的源码区域——页内函数体均为该缩进，map 回调更深缩进
// 不会被误判为边界。
func m55FnRegion(body string, start int) string {
	next := strings.Index(body[start+1:], "\n      function ")
	if next < 0 {
		return body[start:]
	}
	return body[start : start+1+next]
}

// m55Num 安全取数值字段（缺失/非数值时 Fatal，而非 panic）。
func m55Num(t *testing.T, m map[string]any, key, ctx string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s 缺少数值字段 %q: %v", ctx, key, m)
	}
	return v
}

// ---------- T1 花名册积分余额 ----------

// T1 花名册（卡片墙 + 表格双渲染）展示积分余额：loadRoster 的卡片墙渲染段
// （$('roster').innerHTML 之前）与表格渲染段（之后）都必须取用 roster 行的
// currency 字段（JS 证据 s.currency / currency），且渲染文案含「积分」字样。
// 命令: go test ./server/ -run TestIssue55_T1_RosterCurrencyRender -v
func TestIssue55_T1_RosterCurrencyRender(t *testing.T) {
	body := m55Page(t, newHandler(t))

	start := strings.Index(body, "function loadRoster")
	if start < 0 {
		t.Fatalf("/teacher.html 缺少 loadRoster 函数（既有结构被破坏？）")
	}
	region := m55FnRegion(body, start)
	split := strings.Index(region, "$('roster').innerHTML")
	if split < 0 {
		t.Errorf("loadRoster 内未找到表格渲染入口 $('roster').innerHTML（表格视图渲染不得移除）")
		return
	}
	cardSeg, tableSeg := region[:split], region[split:]
	if !m5Contains(cardSeg, "currency") {
		t.Errorf("卡片墙渲染段（loadRoster 前半）缺少 currency 取用：花名册卡片 meta 须展示积分余额（s.currency，M12 roster 新增字段）")
	}
	if !m5Contains(tableSeg, "currency") {
		t.Errorf("表格渲染段（loadRoster 后半）缺少 currency 取用：花名册表格须展示积分余额列（s.currency）")
	}
	if !m5Contains(region, "积分") {
		t.Errorf("loadRoster 渲染文案缺少「积分」字样（积分余额展示须有中文标签）")
	}
}

// ---------- T2 详情页积分余额字段 ----------

// T2 学生详情页显示积分余额：详情视图存在积分余额锚点（id="detail-currency"
// 或等价 id），且 openDetail 函数体内有 currency 赋值逻辑。
// 命令: go test ./server/ -run TestIssue55_T2_DetailCurrencyField -v
func TestIssue55_T2_DetailCurrencyField(t *testing.T) {
	body := m55Page(t, newHandler(t))

	if !m55AnyContains(body, `id="detail-currency"`, `id="detail-balance"`, `id="detail-coin"`) {
		t.Errorf("详情页缺少积分余额元素锚点：期望 id=\"detail-currency\"（或等价 detail-balance/detail-coin）")
	}
	start := strings.Index(body, "function openDetail")
	if start < 0 {
		t.Fatalf("/teacher.html 缺少 openDetail 函数（既有结构被破坏？）")
	}
	if !m5Contains(m55FnRegion(body, start), "currency") {
		t.Errorf("openDetail 函数体缺少 currency 赋值逻辑（详情页积分余额须由 roster/catalog 的 currency 字段填充）")
	}
}

// ---------- T3 皮肤商店区块与三条 API 调用 ----------

// T3 皮肤商店区块（仅已领养学生）调用契约：
//
//	a) 区块容器存在（「皮肤商店」标题或 skin-shop/detail-skins 容器 id 任一）；
//	b) 目录拉取 GET /api/teacher/pets/{studentID}/skins——存在不以 /buy、
//	   /activate 结尾的裸 /skins 调用（计数法：/skins 总次数须大于
//	   /skins/buy + /skins/activate 之和），且以 /api/teacher/pets/ 前缀拼接；
//	c) 购买 POST .../skins/buy 的请求体附近有 requestId（前端生成防重复扣减）；
//	d) 穿戴/取下 POST .../skins/activate 调用存在（空串切回默认）。
//
// 命令: go test ./server/ -run TestIssue55_T3_SkinShopBlockAndAPICalls -v
func TestIssue55_T3_SkinShopBlockAndAPICalls(t *testing.T) {
	body := m55Page(t, newHandler(t))

	// a) 区块容器。
	if !m55AnyContains(body, "皮肤商店", `id="skin-shop"`, `id="detail-skins"`, `id="skin-shop-view"`) {
		t.Errorf("详情页缺少皮肤商店区块（「皮肤商店」标题或 skin-shop/detail-skins 容器 id 任一）")
	}
	// b) 目录 GET：裸 /skins 调用存在。
	total := m55Count(body, "/skins")
	buy := m55Count(body, "/skins/buy")
	act := m55Count(body, "/skins/activate")
	if total <= buy+act {
		t.Errorf("缺少皮肤目录调用：/skins 出现 %d 次均被 /skins/buy(%d) 与 /skins/activate(%d) 占用，未见 GET /api/teacher/pets/{studentID}/skins 目录拉取", total, buy, act)
	}
	if !m5Contains(body, "/api/teacher/pets/") {
		t.Errorf("缺少路径前缀 /api/teacher/pets/ 拼接（皮肤 API 须以 roster 行的数字 s.id 拼 URL，不是 studentNo）")
	}
	// c) 购买 body 含 requestId。
	if w, ok := m55Around(body, "/skins/buy", 300, 900); ok {
		if !m5Contains(w, "requestId") {
			t.Errorf("购买调用 /skins/buy 附近缺少 requestId（购买 body 为 {skinId, requestId}，前端须生成防重复扣减）")
		}
	} else {
		t.Errorf("缺少购买调用路径 /skins/buy（POST /api/teacher/pets/{studentID}/skins/buy）")
	}
	// d) 穿戴/取下调用。
	if !m5Contains(body, "/skins/activate") {
		t.Errorf("缺少穿戴/取下调用路径 /skins/activate（POST /api/teacher/pets/{studentID}/skins/activate，body {skinId}，空串=切回默认）")
	}
}

// ---------- T4 使用中主图换打卡照 ----------

// T4 activeScene 主图替换 + 皮肤图兜底：
//
//	a) openDetail 函数体读取 activeScene（非空时详情主图用该皮肤 imageUrl）；
//	b) openDetail 仍有 imageUrl 主图赋值（既有行为不回退）；
//	c) 皮肤渲染区（首个 /skins 调用附近）的皮肤卡片图带 onerror 兜底回原立绘
//	   （皮肤图 → s.imageUrl → s.silhouette，参考页面既有 onerror 写法）。
//
// 命令: go test ./server/ -run TestIssue55_T4_ActiveSceneSwapsMainImage -v
func TestIssue55_T4_ActiveSceneSwapsMainImage(t *testing.T) {
	body := m55Page(t, newHandler(t))

	start := strings.Index(body, "function openDetail")
	if start < 0 {
		t.Fatalf("/teacher.html 缺少 openDetail 函数（既有结构被破坏？）")
	}
	region := m55FnRegion(body, start)
	if !m5Contains(region, "activeScene") {
		t.Errorf("openDetail 未读取 activeScene：使用中皮肤须把详情主图替换为该皮肤 imageUrl（打卡照）")
	}
	if !m5Contains(region, "imageUrl") {
		t.Errorf("openDetail 缺少 imageUrl 主图赋值（既有默认立绘行为不得回退）")
	}
	// 皮肤渲染区兜底链。
	if w, ok := m55Around(body, "/skins", 400, 6000); ok {
		if !m5Contains(w, "onerror") {
			t.Errorf("皮肤卡片图缺少 onerror 兜底（皮肤图 404 时须回退原立绘）")
		}
		if !m5Contains(w, "silhouette") {
			t.Errorf("皮肤卡片图 onerror 链未兜底到 s.silhouette（应为 皮肤图 → s.imageUrl → s.silhouette）")
		}
	} else {
		t.Errorf("未找到皮肤目录调用 /skins，无法定位皮肤渲染区（主图换打卡照的兜底链无从谈起）")
	}
}

// ---------- T5 未领养学生不展示皮肤商店 ----------

// T5 皮肤商店仅对已领养学生展示：皮肤商店代码区（首个 /skins 调用前后窗口）
// 须存在 adopted 分支证据（未领养不发 catalog 请求/不渲染商店）。
// 命令: go test ./server/ -run TestIssue55_T5_UnadoptedHidesSkinShop -v
func TestIssue55_T5_UnadoptedHidesSkinShop(t *testing.T) {
	body := m55Page(t, newHandler(t))

	if w, ok := m55Around(body, "/skins", 1500, 6000); ok {
		if !m5Contains(w, "adopted") {
			t.Errorf("皮肤商店区域缺少 adopted 分支：未领养学生不得展示皮肤商店（仅已领养学生调 skins API/渲染商店）")
		}
	} else {
		t.Errorf("未找到皮肤目录调用 /skins，无法验证未领养分支")
	}
}

// ---------- T6 加分成功提示含积分入账文案 ----------

// T6 加分双轨呈现：每一处「加分成功」提示（含触发进化分支）的附近文案都须
// 出现「积分」字样（加分 1:1 同步入账 currency 的说明）。
// 命令: go test ./server/ -run TestIssue55_T6_PointsAlertMentionsCurrency -v
func TestIssue55_T6_PointsAlertMentionsCurrency(t *testing.T) {
	body := m55Page(t, newHandler(t))

	if m55Count(body, "加分成功") == 0 {
		t.Fatalf("缺少「加分成功」提示分支（既有 doPoints 行为被删除？）")
	}
	bad, from := 0, 0
	for {
		rel := strings.Index(body[from:], "加分成功")
		if rel < 0 {
			break
		}
		i := from + rel
		lo, hi := i-300, i+700
		if lo < 0 {
			lo = 0
		}
		if hi > len(body) {
			hi = len(body)
		}
		if !m5Contains(body[lo:hi], "积分") {
			bad++
		}
		from = i + len("加分成功")
	}
	if bad > 0 {
		t.Errorf("%d 处「加分成功」提示未提及积分入账（加分 1:1 入账 currency，成功提示须带积分同步说明）", bad)
	}
}

// ---------- T7 既有锚点不回归 + T8 行为结构完整性 ----------

// T7+T8 无回归与结构完整（合并一个用例；红态失败由 T8 末项「皮肤商店加载
// 函数」承担，T7 守护子断言红态本就应全绿——它们只在实现阶段改坏锚点时失败）：
//
//	T7：对照 origin/main:web/static/teacher.html 提取的锚点清单（视图 id、
//	    详情页 id、行内操作 class、data-* 属性）逐一断言仍存在且未改名；
//	T8：<script> IIFE 包裹、api()/escapeHtml()/loadRoster()/openDetail()
//	    既有函数仍在，皮肤商店加载函数（loadSkinShop 或等价命名）已加入，
//	    文件 UTF-8 可解码。
//
// 命令: go test ./server/ -run TestIssue55_T7T8_AnchorsAndStructure -v
func TestIssue55_T7T8_AnchorsAndStructure(t *testing.T) {
	body := m55Page(t, newHandler(t))

	// ---- T7 既有锚点清单（提取自 origin/main:web/static/teacher.html）----
	ids := []string{
		// 视图与导航
		"login-view", "register-view", "work-view", "detail-view", "trash-view", "pass-view",
		"main-nav", "nav-work", "nav-trash", "nav-pass", "btn-logout",
		// 登录/注册
		"login-email", "login-pass", "btn-login", "goto-register", "login-msg",
		"reg-email", "btn-send-code", "reg-code", "reg-pass", "reg-class",
		"btn-register", "goto-login", "reg-msg",
		// 工作台与花名册
		"add-form", "add-name", "add-no", "add-msg",
		"card-wall", "roster-table", "roster", "btn-view-toggle", "btn-refresh", "work-msg",
		// 学生详情（本 Issue 改造主场，锚点不得改名）
		"detail-back", "detail-pet-img", "detail-student-name", "detail-student-no",
		"detail-pet-name", "detail-species-name", "detail-level", "detail-points",
		"detail-adopt", "detail-points-btn", "detail-rename", "detail-delete", "detail-msg",
		// 修改密码（#53）
		"pass-old", "pass-new", "pass-new2", "btn-change-pass", "pass-back", "pass-msg",
		// 垃圾桶
		"trash", "btn-trash-refresh", "trash-msg",
		// 发宠物选择器（#35）
		"adopt-picker", "adopt-close", "adopt-species",
	}
	for _, id := range ids {
		if !m5Contains(body, `id="`+id+`"`) {
			t.Errorf("T7 既有锚点回归：id=%q 在 teacher.html 中缺失或被改名", id)
		}
	}
	classes := []string{
		"do-points", "do-rename", "do-delete", "do-adopt", "do-restore",
		"student-card", "card-meta", "card-pet-img", "card-name", "card-ops",
		"species-card", "species-grid", "pet-thumb", "subview", "rarity-badge",
	}
	for _, c := range classes {
		if !strings.Contains(body, c) {
			t.Errorf("T7 既有锚点回归：class/name=%q 在 teacher.html 中缺失", c)
		}
	}
	for _, d := range []string{"data-label", "data-species", "data-no", "data-id"} {
		if !strings.Contains(body, d) {
			t.Errorf("T7 既有锚点回归：属性 %q 在 teacher.html 中缺失", d)
		}
	}

	// ---- T8 行为结构完整性 ----
	if !utf8.Valid([]byte(body)) {
		t.Errorf("T8 teacher.html 不是合法 UTF-8（解码失败）")
	}
	if !m5Contains(body, "<script>") {
		t.Errorf("T8 缺少 <script> 块")
	}
	if !m5Contains(body, "(function(){") || !m5Contains(body, "})();") {
		t.Errorf("T8 缺少 IIFE 包裹 (function(){...})();")
	}
	for _, fn := range []string{"function api(", "function escapeHtml(", "function loadRoster(", "function openDetail("} {
		if !m5Contains(body, fn) {
			t.Errorf("T8 缺少既有函数 %s", fn)
		}
	}
	// F1 新增结构要求（本用例红态失败点）。
	if !m55AnyContains(body, "function loadSkinShop(", "function loadSkins(",
		"function renderSkins(", "function loadSkinCatalog(", "function loadShop(") {
		t.Errorf("T8 缺少皮肤商店加载函数（loadSkinShop 或等价命名）：详情页须拉取并渲染皮肤目录")
	}
}

// ---------- T9 roster→catalog 字段对齐（真实 httptest 流程） ----------

// T9 前后端字段对齐闭环：登录→加学生→发宠→加分 1 →
//
//	roster 行 currency=1 且含 activeScene 字段；
//	catalog currency=1、skins 恰 6 款（meal/sleep/park/swim/beach/travel）、
//	  每款 price=5 且七字段（skinId/name/price/imageUrl/owned/active/purchasable）齐全；
//	补分至 9 后买 meal → catalog 该款 owned=true active=true、currency=4；
//	activate 空串 → 响应与 catalog 的 activeScene 均为 null；
//	最后闭环：上述契约字段名（currency/activeScene/skinId/owned/purchasable/price）
//	  必须被 teacher.html 的 JS 消费（红态失败点）。
//
// 命令: go test ./server/ -run TestIssue55_T9_RosterCatalogFieldAlignment -v
func TestIssue55_T9_RosterCatalogFieldAlignment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pet.db")
	h := newHandlerAt(t, dbPath)
	token := m6RegisterTeacher(t, h, dbPath, "m55align@example.com", "对齐班")
	st := m6MustCreateStudent(t, h, token, "对齐生", "01")
	resp, decoded := doJSON(t, h, http.MethodPost, "/api/teacher/adopt", token,
		map[string]any{"studentNo": st.StudentNo, "speciesId": "bunny"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adopt 失败: %d %v", resp.StatusCode, decoded)
	}
	m12Add(t, h, token, st.StudentNo, 1)

	// roster：currency=1（1:1 入账），activeScene 字段存在。
	roster := m7Roster(t, h, token)
	row := roster[st.StudentNo]
	if got := m55Num(t, row, "currency", "roster 行"); got != 1 {
		t.Fatalf("roster currency = %v, 期望 1", got)
	}
	if _, ok := row["activeScene"]; !ok {
		t.Fatal("roster 行缺少 activeScene 字段")
	}

	sid := m12StudentID(t, h, token, st.StudentNo)

	// catalog：currency=1、6 款、price=5、字段齐全。
	if code, cat := m12Skins(t, h, token, sid); code != http.StatusOK {
		t.Fatalf("skins catalog 状态码 = %d, 期望 200", code)
	} else {
		if got := m55Num(t, cat, "currency", "catalog"); got != 1 {
			t.Fatalf("catalog currency = %v, 期望 1", got)
		}
		skins, ok := cat["skins"].([]any)
		if !ok || len(skins) != 6 {
			t.Fatalf("catalog skins 长度 = %d, 期望 6", len(skins))
		}
		want := map[string]bool{"meal": false, "sleep": false, "park": false, "swim": false, "beach": false, "travel": false}
		for _, s := range skins {
			m, _ := s.(map[string]any)
			id, _ := m["skinId"].(string)
			if got := m55Num(t, m, "price", "skin "+id); got != 5 {
				t.Fatalf("skin %s price = %v, 期望 5", id, got)
			}
			for _, k := range []string{"skinId", "name", "imageUrl", "owned", "active", "purchasable"} {
				if _, ok := m[k]; !ok {
					t.Errorf("skin %s 缺少字段 %q", id, k)
				}
			}
			if _, known := want[id]; known {
				want[id] = true
			}
		}
		for id, seen := range want {
			if !seen {
				t.Errorf("catalog 缺少固定款 %q", id)
			}
		}
	}

	// 补 8 分（合计 9）→ 买 meal（扣 5）→ catalog：owned+active、currency=4。
	m12Add(t, h, token, st.StudentNo, 8)
	if code, body := m12Buy(t, h, token, sid, "meal", "m55-buy-meal"); code != http.StatusOK || body["purchased"] != true {
		t.Fatalf("购买 meal 应 200/purchased=true: %d %v", code, body)
	}
	_, cat := m12Skins(t, h, token, sid)
	if got := m55Num(t, cat, "currency", "购买后 catalog"); got != 4 {
		t.Fatalf("购买后 catalog currency = %v, 期望 4", got)
	}
	var meal map[string]any
	for _, s := range cat["skins"].([]any) {
		if m, _ := s.(map[string]any); m["skinId"] == "meal" {
			meal = m
		}
	}
	if meal == nil || meal["owned"] != true || meal["active"] != true {
		t.Fatalf("购买后 catalog 中 meal 应 owned=true active=true: %v", meal)
	}

	// 取下（activate 空串）→ activeScene=null。
	if code, body := m12Activate(t, h, token, sid, ""); code != http.StatusOK || body["activeScene"] != nil {
		t.Fatalf("切回默认应 200/activeScene=null: %d %v", code, body)
	}
	_, cat = m12Skins(t, h, token, sid)
	if cat["activeScene"] != nil {
		t.Fatalf("取下后 catalog activeScene = %v, 期望 null", cat["activeScene"])
	}

	// 前端闭环：契约字段名必须被 teacher.html 消费（红态失败点）。
	page := m55Page(t, h)
	for _, field := range []string{"currency", "activeScene", "skinId", "owned", "purchasable", "price"} {
		if !m5Contains(page, field) {
			t.Errorf("teacher.html 未消费契约字段 %q（roster/catalog 下发字段须在前端 JS 中取用）", field)
		}
	}
}

// ---------- T10 前端调用路径与路由注册一致 ----------

// T10 teacher.html 中出现的 skins 相关 fetch 路径须与 server.go 的 mux 注册
// 逐一对齐：解析 server.go 中全部 "/api/teacher/pets/{studentID}/skins*"
// 注册（method + path 模板），{studentID} 占位后的路径后缀（/skins、
// /skins/buy、/skins/activate）必须能在 teacher.html 源码中找到对应调用字面量。
// 命令: go test ./server/ -run TestIssue55_T10_FetchPathsMatchRegisteredRoutes -v
func TestIssue55_T10_FetchPathsMatchRegisteredRoutes(t *testing.T) {
	body := m55Page(t, newHandler(t))

	src, err := os.ReadFile("server.go")
	if err != nil {
		src, err = os.ReadFile(filepath.Join("..", "server", "server.go"))
		if err != nil {
			t.Fatalf("读取 server.go 失败: %v", err)
		}
	}
	re := regexp.MustCompile(`"(GET|POST|PUT|PATCH|DELETE) (/api/teacher/pets/\{studentID\}/skins[^"]*)"`)
	routes := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		routes[m[1]+" "+m[2]] = true
	}
	if len(routes) < 3 {
		t.Fatalf("server.go 中 skins 路由注册数 = %d, 期望 ≥3（catalog/buy/activate——M12 后端应已合入）", len(routes))
	}
	if !m5Contains(body, "/api/teacher/pets/") {
		t.Errorf("teacher.html 缺少路径前缀 /api/teacher/pets/（皮肤 API 按数字 student id 拼接 URL）")
	}
	suffixes := make([]string, 0, len(routes))
	for route := range routes {
		path := strings.SplitN(route, " ", 2)[1]
		suffixes = append(suffixes, strings.TrimPrefix(path, "/api/teacher/pets/{studentID}"))
	}
	sort.Strings(suffixes)
	for _, suffix := range suffixes {
		if !m5Contains(body, suffix) {
			t.Errorf("teacher.html 缺少与 server.go 路由注册对应的调用路径后缀 %q（前端 fetch 路径须与 mux 注册一致）", suffix)
		}
	}
}
