package server_test

// Issue #35 M8 UI/UE 改版测试（U1 卡片墙首屏 / U2 学生详情 / U3 带图选择器 / 移动端与守护）。
// 契约来源：测试先行子智能体用例 T1–T16（D1 已批准：roster 补学生数字 id，T16 纳入）。

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// ---------- U1 卡片墙 ----------

func TestM8_U1_CardWallContainer(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`id="card-wall"`,
		`.subview { display: none; }`,
		`.subview.active { display: block; }`,
		`.card-wall { display: grid`,
		`grid-template-columns: repeat(auto-fill, minmax(220px, 1fr))`,
		`.student-card {`,
		`.card-pet-img {`,
		`object-fit: contain`,
		`.card-name { font-size: 20px`,
		`.card-meta { font-size: 13px`,
		`color: #8a97a6`,
		`.card-ops {`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少卡片墙锚点 %q", anchor)
		}
	}
}

func TestM8_U1_CardRender(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`$('card-wall').innerHTML`,
		`$('roster').innerHTML`,
		`class="card-pet-img"`,
		`s.imageUrl`,
		`this.onerror=null`,
		`this.src=`,
		`s.silhouette`,
		`eggs/common.png`,
		`未领养`,
		`s.name`,
		`s.studentNo`,
		`Lv`,
		`s.points`,
		`do-adopt`,
		`do-points`,
		`do-rename`,
		`do-delete`,
		`$('card-wall').addEventListener('click'`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少卡片渲染锚点 %q", anchor)
		}
	}
}

func TestM8_U1_ViewToggleAndTableKept(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	// 默认视图：card-wall 带 active、roster-table 不带
	wallRe := regexp.MustCompile(`<div[^>]*id="card-wall"[^>]*>`)
	if m := wallRe.FindStringSubmatch(body); m == nil || !strings.Contains(m[0], "active") {
		t.Errorf("card-wall 应默认带 active（默认卡片墙）")
	}
	tableRe := regexp.MustCompile(`<div[^>]*id="roster-table"[^>]*>`)
	if m := tableRe.FindStringSubmatch(body); m == nil || strings.Contains(m[0], "active") {
		t.Errorf("roster-table 应默认不带 active")
	}

	for _, anchor := range []string{
		`id="btn-view-toggle"`,
		`$('btn-view-toggle')`,
		`表格视图`,
		`<table>`,
		`<thead>`,
		`学号`, `姓名`, `宠物`, `等级`, `积分`, `最近加分`, `操作`,
		`<tbody id="roster">`,
		`data-label`,
		`pet-thumb`,
		`id="add-form"`,
		`id="add-name"`,
		`id="add-no"`,
		`$('add-form').addEventListener('submit'`,
		`id="btn-refresh"`,
		`id="work-msg"`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少表格/表单保留锚点 %q", anchor)
		}
	}
}

func TestM8_U1_RootServesSameSPA(t *testing.T) {
	h := newHandler(t)
	root := m5Get(t, h, "/")
	page := m5Get(t, h, "/teacher.html")
	for _, anchor := range []string{`id="card-wall"`, `id="detail-view"`, `id="adopt-picker"`} {
		if !m5Contains(root, anchor) || !m5Contains(page, anchor) {
			t.Errorf("路径 %q 缺少锚点 %q", "/", anchor)
		}
	}
}

// ---------- U2 学生详情 ----------

func TestM8_U2_DetailView(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`<div class="view" id="detail-view">`,
		`id="detail-pet-img"`,
		`.detail-pet-img { width: 240px`,
		`height: 240px`,
		`object-fit: contain`,
		`id="detail-student-name"`, `$('detail-student-name')`,
		`id="detail-student-no"`, `$('detail-student-no')`,
		`id="detail-pet-name"`, `$('detail-pet-name')`,
		`id="detail-species-name"`, `$('detail-species-name')`,
		`id="detail-level"`, `$('detail-level')`,
		`id="detail-points"`, `$('detail-points')`,
		`id="detail-points-btn"`,
		`id="detail-rename"`, `$('detail-rename')`,
		`id="detail-delete"`, `$('detail-delete')`,
		`id="detail-adopt"`,
		`id="detail-back"`, `$('detail-back')`,
		`show('work-view')`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少详情锚点 %q", anchor)
		}
	}
}

func TestM8_U2_DetailLogicNoNewAPI(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	if !m5Contains(body, `function openDetail(`) {
		t.Errorf("缺少 openDetail 函数定义")
	}
	for _, anchor := range []string{
		`var rosterCache = {}`,
		`rosterCache[s.studentNo] = s`,
		`var s = rosterCache[no];`,
		`eggs/common.png`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少详情逻辑锚点 %q", anchor)
		}
	}

	// 审查补测：doRename/doDelete 错误提示按视图路由（工作台侧 work-msg 回退）
	for _, fn := range []string{"function doRename(no)", "function doDelete(no)"} {
		i := strings.Index(body, fn)
		if i < 0 {
			t.Errorf("缺少函数 %s", fn)
			continue
		}
		seg := body[i:]
		if j := strings.Index(seg, "\n      function "); j >= 0 {
			seg = seg[:j]
		}
		if !strings.Contains(seg, "detailNo === no") || !strings.Contains(seg, "$('work-msg')") {
			t.Errorf("%s 错误提示未按视图路由（缺 detailNo 分流或 work-msg 回退）", fn)
		}
	}

	// 端点白名单：不得出现新端点
	re := regexp.MustCompile(`/api/teacher/[A-Za-z0-9/_-]*`)
	seen := map[string]bool{}
	for _, ep := range re.FindAllString(body, -1) {
		seen[ep] = true
	}
	allowed := []string{
		"/api/teacher/email-code", "/api/teacher/register", "/api/teacher/login",
		"/api/teacher/roster", "/api/teacher/adopt", "/api/teacher/points",
		"/api/teacher/pets/", "/api/teacher/students", "/api/teacher/students/",
		"/api/teacher/trash",
	}
	for ep := range seen {
		ok := false
		for _, a := range allowed {
			if strings.HasPrefix(ep, strings.TrimRight(a, "/")) || ep == strings.TrimRight(a, "/") {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("出现白名单外端点字面量: %q", ep)
		}
	}
}

// ---------- U3 带图选择器 ----------

func TestM8_U3_SpeciesPicker(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`id="adopt-picker"`,
		`.adopt-picker { display: none`,
		`.adopt-picker.open { display`,
		`.species-grid { display: grid`,
		`id="adopt-close"`,
		`data-species=""`,
		`eggs/common.png`,
		`随机`,
		`.rarity-common { background: #8a97a6`,
		`.rarity-rare { background: #2f80ed`,
		`.rarity-epic { background: #f5a623`,
		`普通`, `稀有`, `史诗`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少选择器锚点 %q", anchor)
		}
	}

	// 12 物种逐个：data-species、名称、阶段 2 图 URL、稀有度徽标
	type sp struct{ id, name, rarity string }
	list := []sp{
		{"cat", "电力猫", "common"}, {"dog", "像素狗", "common"},
		{"bunny", "云绒兔", "common"}, {"hamster", "芯片仓鼠", "common"},
		{"chick", "蛋壳鸡", "common"}, {"penguin", "冰川企鹅", "common"},
		{"koala", "电池考拉", "common"}, {"axolotl", "六角恐龙", "common"},
		{"fox", "星辰狐", "rare"}, {"panda", "太极熊猫", "rare"},
		{"dino", "机械恐龙", "rare"}, {"dragon", "神威小龙", "epic"},
	}
	for _, s := range list {
		card := fmt.Sprintf(`data-species="%s"`, s.id)
		if !m5Contains(body, card) {
			t.Errorf("选择器缺少物种卡 %q", card)
		}
		if !m5Contains(body, s.name) {
			t.Errorf("选择器缺少名称 %q", s.name)
		}
		img := fmt.Sprintf("https://pet-aibbm-assets.oss-cn-hangzhou.aliyuncs.com/pets/%s/2.png", s.id)
		if !m5Contains(body, img) {
			t.Errorf("选择器缺少阶段图 %q", img)
		}
		badge := fmt.Sprintf("rarity-%s", s.rarity)
		if !m5Contains(body, badge) {
			t.Errorf("选择器缺少稀有度徽标 %q", badge)
		}
	}
}

func TestM8_U3_AdoptSubmitLogic(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`getAttribute('data-species')`,
		`$('adopt-species').value`,
		`adoptBody.speciesId = speciesId`,
		`if (speciesId)`,
		`$('adopt-picker').classList.add('open')`,
		`speciesId`,
		`adopt-species`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少选择器提交逻辑锚点 %q", anchor)
		}
	}
}

// ---------- M7 前端测试兼容性（原样 PASS 的证明由 go test 给出） ----------

func TestM8_M7FrontendAnchorsSurvive(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	// TestM7_W2_TeacherHTMLSpeciesPicker 全部锚点
	for _, anchor := range []string{
		`id="adopt-species"`,
		`<option value="">随机</option>`,
		`value="cat"`, `value="dog"`, `value="bunny"`, `value="hamster"`,
		`value="chick"`, `value="penguin"`, `value="koala"`, `value="axolotl"`,
		`value="fox"`, `value="panda"`, `value="dino"`, `value="dragon"`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("M7 SpeciesPicker 锚点丢失: %q", anchor)
		}
	}
	// TestM7_W1_TeacherHTMLPetImages 关键锚点
	for _, anchor := range []string{
		`class="pet-thumb"`,
		`this.onerror=null`,
		`this.src=`,
		`s.imageUrl`,
		`s.silhouette`,
		`<th>宠物</th>`,
		`colspan="5"`,
		`无宠物`,
		`未领养`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("M7 PetImages 锚点丢失: %q", anchor)
		}
	}
}

// ---------- 移动端 ----------

func TestM8_U4_MediaAdaptations(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	if n := strings.Count(body, "@media"); n != 1 {
		t.Fatalf("@media 应恰好出现 1 次, 实际 %d 次", n)
	}
	start := strings.Index(body, "@media (max-width: 1023px)")
	if start < 0 {
		t.Fatalf("缺少 @media (max-width: 1023px) 块")
	}
	media := body[start:]
	for _, anchor := range []string{
		`.card-wall { grid-template-columns: 1fr`,
		`.detail-pet-img { max-width: 100%`,
		`.species-grid { grid-template-columns: repeat(2, 1fr)`,
		`min-height: 44px`,
		`font-size: 16px`,
		`attr(data-label)`,
		`data-label`,
	} {
		if !m5Contains(media, anchor) {
			t.Errorf("@media 块缺少适配规则 %q", anchor)
		}
	}
}

func TestM8_U4_MediaBlockTailTeacherHTML(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")
	start := strings.Index(body, "<style>")
	end := strings.Index(body, "</style>")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("teacher.html 缺少 <style> 块")
	}
	css := body[start+len("<style>") : end]

	mi := strings.Index(css, "@media")
	if mi < 0 {
		t.Fatalf("内联 CSS 缺少 @media 块")
	}
	depth := 0
	closed := -1
	for i := mi; i < len(css); i++ {
		switch css[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				closed = i
				i = len(css)
			}
		}
	}
	if closed < 0 {
		t.Fatalf("@media 块未正确闭合")
	}
	tail := strings.TrimSpace(css[closed+1:])
	tail = strings.TrimSuffix(tail, "</style>")
	tail = strings.TrimSpace(tail)
	tail = strings.ReplaceAll(tail, "<!--", "/*")
	tail = strings.ReplaceAll(tail, "-->", "*/")
	if tail != "" {
		t.Errorf("@media 块收尾后存在残留 CSS 规则: %q", tail[:min(len(tail), 120)])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------- T16（D1 批准）：roster 行携带学生数字 id ----------

func TestM8_D1_RosterCarriesStudentID(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, fmt.Sprintf("m8d1%d@example.com", systemMillis()), "M8D1班")

	s1 := m6MustCreateStudent(t, h, token, "带宠", "D01")
	s2 := m6MustCreateStudent(t, h, token, "无宠", "D02")
	if status, _, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "D01", "speciesId": "fox"}); status != http.StatusOK {
		t.Fatalf("D01 adopt(fox) 状态码 = %d; body=%v", status, body)
	}

	roster := m7Roster(t, h, token)
	r1 := roster["D01"]
	if r1 == nil {
		t.Fatalf("花名册缺少 D01")
	}
	id1f, ok := r1["id"]
	if !ok {
		t.Fatalf("roster D01 行缺少 id 字段")
	}
	id1, ok := id1f.(float64)
	if !ok || id1 <= 0 {
		t.Fatalf("roster D01 id = %v, 期望 >0 数字", id1f)
	}
	if int64(id1) != s1.ID {
		t.Fatalf("roster D01 id = %d, 期望与创建响应一致 %d", int64(id1), s1.ID)
	}

	r2 := roster["D02"]
	if r2 == nil {
		t.Fatalf("花名册缺少 D02")
	}
	id2f, ok := r2["id"]
	if !ok {
		t.Fatalf("roster D02 行缺少 id 字段（未领养行同样携带）")
	}
	id2, ok := id2f.(float64)
	if !ok || id2 <= 0 {
		t.Fatalf("roster D02 id = %v, 期望 >0 数字", id2f)
	}
	if int64(id2) != s2.ID {
		t.Fatalf("roster D02 id = %d, 期望与创建响应一致 %d", int64(id2), s2.ID)
	}
}
