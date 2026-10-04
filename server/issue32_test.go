package server_test

// Issue #32 M7 教师端反馈修复包测试（W1 图片下发 / W2 发宠自选物种 / W3 发码文案）。
// 契约来源：测试先行子智能体用例 T1–T8；T9/T10（go test / go vet）与 T11（diff 范围）为命令行检查。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const m7OSSBase = "https://pet-aibbm-assets.oss-cn-hangzhou.aliyuncs.com"

// m7Do2 是 m6Do 的两返回值封装（丢弃原始响应体）。
func m7Do2(h http.Handler, method, target, token string, body any) (int, map[string]any) {
	status, decoded, _ := m6Do(h, method, target, token, body)
	return status, decoded
}

func systemMillis() int64 { return time.Now().UnixMilli() }

// m7SpeciesImageURLOnly 为 roster/trash 行的图片字段形状。
type m7SpeciesImageFields struct {
	ImageURL   string `json:"imageUrl"`
	Silhouette string `json:"silhouette"`
}

// m7AdoptEnv 解码 adopt 响应的 pet.species 全量字段（rarity/imageUrl/silhouette）。
type m7AdoptEnv struct {
	Pet struct {
		Species struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Rarity     string `json:"rarity"`
			ImageURL   string `json:"imageUrl"`
			Silhouette string `json:"silhouette"`
		} `json:"species"`
		Level  int `json:"level"`
		Points int `json:"points"`
	} `json:"pet"`
}

// m7AdoptWith 调 POST /api/teacher/adopt，body 原样提交（speciesId 由调用方控制有无）。
func m7AdoptWith(t *testing.T, h http.Handler, token string, body map[string]any) (int, m7AdoptEnv, map[string]any) {
	t.Helper()
	status, decoded, raw := m6Do(h, http.MethodPost, "/api/teacher/adopt", token, body)
	var env m7AdoptEnv
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解码 adopt 响应失败: %v（raw=%s）", err, raw)
	}
	return status, env, decoded
}

// m7Roster 拉花名册并返回 studentNo → 行（含 imageUrl/silhouette）。
func m7Roster(t *testing.T, h http.Handler, token string) map[string]map[string]any {
	t.Helper()
	status, decoded, raw := m6Do(h, http.MethodGet, "/api/teacher/roster", token, nil)
	if status != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, 期望 200; body=%s", status, raw)
	}
	rows, ok := decoded["students"].([]any)
	if !ok {
		t.Fatalf("roster 响应缺少 students 数组: %s", raw)
	}
	out := make(map[string]map[string]any)
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("roster 行形状异常: %v", r)
		}
		no, _ := m["studentNo"].(string)
		out[no] = m
	}
	return out
}

// m7Trash 拉垃圾桶并返回 studentNo → 行。
func m7Trash(t *testing.T, h http.Handler, token string) map[string]map[string]any {
	t.Helper()
	status, decoded, raw := m6Do(h, http.MethodGet, "/api/teacher/trash", token, nil)
	if status != http.StatusOK {
		t.Fatalf("trash 状态码 = %d, 期望 200; body=%s", status, raw)
	}
	rows, ok := decoded["items"].([]any)
	if !ok {
		t.Fatalf("trash 响应缺少 items 数组: %s", raw)
	}
	out := make(map[string]map[string]any)
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("trash 行形状异常: %v", r)
		}
		no, _ := m["studentNo"].(string)
		out[no] = m
	}
	return out
}

// m7PetCount 直查 pets 表行数（校验非法 speciesId 不得落库）。
func m7PetCount(t *testing.T, dbPath string) int {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pets`).Scan(&n); err != nil {
		t.Fatalf("查 pets 计数失败: %v", err)
	}
	return n
}

// m7AddPoints 给学生加分（幂等 requestId 自动生成）。
func m7AddPoints(t *testing.T, h http.Handler, token, studentNo string, value int) {
	t.Helper()
	req := m6PointsReq{
		StudentNo: studentNo,
		Reason:    "测试加分",
		Value:     value,
		RequestID: fmt.Sprintf("m7-%s-%d-%d", studentNo, value, systemMillis()),
	}
	status, _, _, body := m6AddPointsFor(h, token, req)
	if status != http.StatusOK {
		t.Fatalf("加分(%s +%d) 状态码 = %d, 期望 200; body=%v", studentNo, value, status, body)
	}
}

// m7DeleteStudent / m7RestoreStudent 删除与恢复（走 M6 端点）。
func m7DeleteStudent(t *testing.T, h http.Handler, token string, id int64) {
	t.Helper()
	status, body := m7Do2(h, http.MethodDelete, fmt.Sprintf("/api/teacher/students/%d", id), token, nil)
	if status != http.StatusOK {
		t.Fatalf("删除学生(%d) 状态码 = %d, 期望 200; body=%v", id, status, body)
	}
}

func m7RestoreStudent(t *testing.T, h http.Handler, token string, id int64) {
	t.Helper()
	status, body := m7Do2(h, http.MethodPost, fmt.Sprintf("/api/teacher/students/%d/restore", id), token, nil)
	if status != http.StatusOK {
		t.Fatalf("恢复学生(%d) 状态码 = %d, 期望 200; body=%v", id, status, body)
	}
}

// m7Test 采用“指定物种”领养的 12 个 canonical 物种（id → 稀有度）。
var m7SpeciesRarity = map[string]string{
	"cat":     "common",
	"dog":     "common",
	"bunny":   "common",
	"hamster": "common",
	"chick":   "common",
	"penguin": "common",
	"koala":   "common",
	"axolotl": "common",
	"fox":     "rare",
	"panda":   "rare",
	"dino":    "rare",
	"dragon":  "epic",
}

// ---------- T1：W1 API——roster/trash 下发 imageUrl/silhouette 与等级→阶段映射 ----------

func TestM7_W1_RosterTrashImageFields(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "m7w1@example.com", "M7W1班")

	m6MustCreateStudent(t, h, token, "生一", "01")
	s02 := m6MustCreateStudent(t, h, token, "生二", "02")
	s03 := m6MustCreateStudent(t, h, token, "生三", "03")
	m6MustCreateStudent(t, h, token, "生四", "04")
	m6MustCreateStudent(t, h, token, "生五", "05")

	// 01：指定 fox，升到等级 2 → 阶段图 2.png
	if status, env, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "01", "speciesId": "fox"}); status != http.StatusOK {
		t.Fatalf("01 adopt(fox) 状态码 = %d; body=%v", status, body)
	} else if env.Pet.Species.ID != "fox" || env.Pet.Species.Rarity != "rare" {
		t.Fatalf("01 adopt(fox) 物种=%s 稀有度=%s, 期望 fox/rare", env.Pet.Species.ID, env.Pet.Species.Rarity)
	}
	m7AddPoints(t, h, token, "01", 20)

	// 02：指定 dragon（等级 1）→ 删除进垃圾桶 → 校验 trash → 恢复
	if status, _, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "02", "speciesId": "dragon"}); status != http.StatusOK {
		t.Fatalf("02 adopt(dragon) 状态码 = %d; body=%v", status, body)
	}
	m7DeleteStudent(t, h, token, s02.ID)
	trash := m7Trash(t, h, token)
	t02, ok := trash["02"]
	if !ok {
		t.Fatalf("垃圾桶缺少 02: %v", trash)
	}
	if got, _ := t02["imageUrl"].(string); got != m7OSSBase+"/pets/dragon/1.png" {
		t.Fatalf("trash 02 imageUrl = %q, 期望 .../pets/dragon/1.png", got)
	}
	if got, _ := t02["silhouette"].(string); got != m7OSSBase+"/pets/dragon/silhouette.png" {
		t.Fatalf("trash 02 silhouette = %q, 期望 .../pets/dragon/silhouette.png", got)
	}
	m7RestoreStudent(t, h, token, s02.ID)

	// 03：未领养直接删除 → trash 图字段为空串
	m7DeleteStudent(t, h, token, s03.ID)
	trash = m7Trash(t, h, token)
	t03, ok := trash["03"]
	if !ok {
		t.Fatalf("垃圾桶缺少 03: %v", trash)
	}
	if got, _ := t03["imageUrl"].(string); got != "" {
		t.Fatalf("trash 03 imageUrl = %q, 期望空串（未领养）", got)
	}
	if got, _ := t03["silhouette"].(string); got != "" {
		t.Fatalf("trash 03 silhouette = %q, 期望空串（未领养）", got)
	}

	// 04：指定 panda，加 50+10=60 分 → 等级 3 封顶 → 阶段图 3.png
	if status, _, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "04", "speciesId": "panda"}); status != http.StatusOK {
		t.Fatalf("04 adopt(panda) 状态码 = %d; body=%v", status, body)
	}
	m7AddPoints(t, h, token, "04", 50)
	m7AddPoints(t, h, token, "04", 10)

	roster := m7Roster(t, h, token)
	r01 := roster["01"]
	if r01 == nil {
		t.Fatalf("花名册缺少 01")
	}
	if adopted, _ := r01["adopted"].(bool); !adopted {
		t.Fatalf("01 adopted 应为 true")
	}
	if got, _ := r01["imageUrl"].(string); got != m7OSSBase+"/pets/fox/2.png" {
		t.Fatalf("roster 01 imageUrl = %q, 期望 .../pets/fox/2.png（等级 2）", got)
	}
	if got, _ := r01["silhouette"].(string); got != m7OSSBase+"/pets/fox/silhouette.png" {
		t.Fatalf("roster 01 silhouette = %q, 期望 .../pets/fox/silhouette.png", got)
	}

	r02 := roster["02"]
	if r02 == nil {
		t.Fatalf("花名册缺少 02（恢复后应在册）")
	}
	if got, _ := r02["imageUrl"].(string); got != m7OSSBase+"/pets/dragon/1.png" {
		t.Fatalf("roster 02 恢复后 imageUrl = %q, 期望 .../pets/dragon/1.png", got)
	}

	r04 := roster["04"]
	if r04 == nil {
		t.Fatalf("花名册缺少 04")
	}
	if got, _ := r04["imageUrl"].(string); got != m7OSSBase+"/pets/panda/3.png" {
		t.Fatalf("roster 04 imageUrl = %q, 期望 .../pets/panda/3.png（等级 3 封顶）", got)
	}

	// 05：在册未领养 → roster 图字段空串
	r05 := roster["05"]
	if r05 == nil {
		t.Fatalf("花名册缺少 05")
	}
	if got, _ := r05["imageUrl"].(string); got != "" {
		t.Fatalf("roster 05 imageUrl = %q, 期望空串（未领养）", got)
	}
	if got, _ := r05["silhouette"].(string); got != "" {
		t.Fatalf("roster 05 silhouette = %q, 期望空串（未领养）", got)
	}

}

// ---------- T2：W1 前端——teacher.html 渲染锚点 ----------

func TestM7_W1_TeacherHTMLPetImages(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`class="pet-thumb"`,
		`this.onerror=null`,
		`this.src=`,
		`s.imageUrl`,
		`s.silhouette`,
		`<th>宠物</th>`,
		`colspan="5"`,
		`无宠物`,
		`.pet-thumb {`,
		`width: 36px`,
		`height: 36px`,
		`object-fit: contain`,
		`border-radius: 8px`,
		`未领养`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少锚点 %q", anchor)
		}
	}
}

// ---------- T3：W2 API——12 物种指定成功 ----------

func TestM7_W2_AdoptSpeciesSpecified(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, fmt.Sprintf("m7w2a%d@example.com", systemMillis()), "M7W2A班")

	ids := make([]string, 0, len(m7SpeciesRarity))
	for id := range m7SpeciesRarity {
		ids = append(ids, id)
	}
	// 排序保证确定性（map 遍历随机）
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}

	for i, id := range ids {
		no := fmt.Sprintf("S%02d", i+1)
		m6MustCreateStudent(t, h, token, "生"+no, no)
		status, env, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": no, "speciesId": id})
		if status != http.StatusOK {
			t.Fatalf("adopt(%s→%s) 状态码 = %d; body=%v", no, id, status, body)
		}
		if env.Pet.Species.ID != id {
			t.Fatalf("adopt(%s→%s) 实际物种 = %s", no, id, env.Pet.Species.ID)
		}
		if env.Pet.Level != 1 || env.Pet.Points != 0 {
			t.Fatalf("adopt(%s→%s) level=%d points=%d, 期望 1/0", no, id, env.Pet.Level, env.Pet.Points)
		}
		wantRarity := m7SpeciesRarity[id]
		if env.Pet.Species.Rarity != wantRarity {
			t.Fatalf("adopt(%s→%s) rarity = %s, 期望 %s（指定物种稀有度随物种自身，权重不适用）",
				no, id, env.Pet.Species.Rarity, wantRarity)
		}
		wantImg := fmt.Sprintf("%s/pets/%s/1.png", m7OSSBase, id)
		if env.Pet.Species.ImageURL != wantImg {
			t.Fatalf("adopt(%s→%s) imageUrl = %q, 期望 %q", no, id, env.Pet.Species.ImageURL, wantImg)
		}
		wantSil := fmt.Sprintf("%s/pets/%s/silhouette.png", m7OSSBase, id)
		if env.Pet.Species.Silhouette != wantSil {
			t.Fatalf("adopt(%s→%s) silhouette = %q, 期望 %q", no, id, env.Pet.Species.Silhouette, wantSil)
		}
	}
	// dragon 指定为 epic 的锚点显式复核（ epic 概率仅 5%，随机几乎不可能指定到）
	if m7SpeciesRarity["dragon"] != "epic" {
		t.Fatalf("契约自检失败：dragon 应为 epic")
	}
}

// ---------- T4：W2 API——随机路径向后兼容 ----------

func TestM7_W2_AdoptRandomCompat(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, fmt.Sprintf("m7w2b%d@example.com", systemMillis()), "M7W2B班")

	// 不含 speciesId 字段
	m6MustCreateStudent(t, h, token, "随一", "R01")
	status, env, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "R01"})
	if status != http.StatusOK {
		t.Fatalf("R01 adopt（无字段）状态码 = %d; body=%v", status, body)
	}
	if !m7ValidSpecies(env.Pet.Species.ID) {
		t.Fatalf("R01 随机物种 %q 不在 canonical 集合", env.Pet.Species.ID)
	}

	// 空串与纯空白等同不传
	m6MustCreateStudent(t, h, token, "随二", "R02")
	if status, _, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "R02", "speciesId": ""}); status != http.StatusOK {
		t.Fatalf("R02 adopt(空串) 状态码 = %d; body=%v", status, body)
	}
	m6MustCreateStudent(t, h, token, "随三", "R03")
	if status, _, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "R03", "speciesId": "   "}); status != http.StatusOK {
		t.Fatalf("R03 adopt(空白) 状态码 = %d; body=%v", status, body)
	}

	// 40 次随机 → 至少覆盖 2 个物种
	seen := map[string]bool{}
	seen[env.Pet.Species.ID] = true
	for i := 1; i <= 40; i++ {
		no := fmt.Sprintf("R%03d", 10+i)
		m6MustCreateStudent(t, h, token, "随"+no, no)
		status, env2, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": no})
		if status != http.StatusOK {
			t.Fatalf("随机 adopt(%s) 状态码 = %d; body=%v", no, status, body)
		}
		if !m7ValidSpecies(env2.Pet.Species.ID) {
			t.Fatalf("随机物种 %q 不在 canonical 集合", env2.Pet.Species.ID)
		}
		seen[env2.Pet.Species.ID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("40 次随机仅覆盖 %d 个物种，随机路径疑似失效", len(seen))
	}
}

func m7ValidSpecies(id string) bool {
	_, ok := m7SpeciesRarity[id]
	return ok
}

// ---------- T5：W2 API——非法 speciesId → 400 不落库 ----------

func TestM7_W2_AdoptSpeciesInvalid(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, fmt.Sprintf("m7w2c%d@example.com", systemMillis()), "M7W2C班")

	invalids := []string{"unicorn", "CAT", "猫", "1", "cat' OR '1'='1", "cat dog"}
	for i, bad := range invalids {
		no := fmt.Sprintf("X%02d", i+1)
		m6MustCreateStudent(t, h, token, "生"+no, no)
		status, body := m7Do2(h, http.MethodPost, "/api/teacher/adopt", token,
			map[string]any{"studentNo": no, "speciesId": bad})
		if status != http.StatusBadRequest {
			t.Fatalf("adopt(非法 %q) 状态码 = %d, 期望 400; body=%v", bad, status, body)
		}
		if got, _ := body["error"].(string); got != "speciesId 不合法，必须是支持的物种之一" {
			t.Fatalf("adopt(非法 %q) error = %q, 期望精确文案", bad, got)
		}
	}

	// 全部非法请求后：pets 表必须为 0 行（校验先于落库）
	if n := m7PetCount(t, dbPath); n != 0 {
		t.Fatalf("非法 speciesId 请求后 pets 表行数 = %d, 期望 0（不得先建宠）", n)
	}

	// 合法重发无脏状态
	if status, env, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "X01", "speciesId": "cat"}); status != http.StatusOK {
		t.Fatalf("X01 重发 adopt(cat) 状态码 = %d; body=%v", status, body)
	} else if env.Pet.Species.ID != "cat" {
		t.Fatalf("X01 重发物种 = %s, 期望 cat", env.Pet.Species.ID)
	}

	// 优先级锚点：speciesId 校验先于学生查询
	status, body := m7Do2(h, http.MethodPost, "/api/teacher/adopt", token,
		map[string]any{"studentNo": "不存在", "speciesId": "unicorn"})
	if status != http.StatusBadRequest {
		t.Fatalf("不存在学号+非法 speciesId 状态码 = %d, 期望 400（speciesId 先校验）; body=%v", status, body)
	}
	if got, _ := body["error"].(string); !strings.Contains(got, "speciesId") {
		t.Fatalf("优先级锚点 error = %q, 期望指向 speciesId", got)
	}
}

// ---------- T6：W2 API——鉴权/冲突/跨班行为不变 ----------

func TestM7_W2_AdoptAuthAndConflict(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, fmt.Sprintf("m7w2d%d@example.com", systemMillis()), "M7W2D班")
	teacherB := m6RegisterTeacher(t, h, dbPath, fmt.Sprintf("m7w2e%d@example.com", systemMillis()), "M7W2E班")

	s1 := m6MustCreateStudent(t, h, token, "已领", "A01")
	if status, _, body := m7AdoptWith(t, h, token, map[string]any{"studentNo": "A01", "speciesId": "fox"}); status != http.StatusOK {
		t.Fatalf("A01 adopt(fox) 状态码 = %d; body=%v", status, body)
	}

	// 无 token → 401
	status, body := m7Do2(h, http.MethodPost, "/api/teacher/adopt", "",
		map[string]any{"studentNo": "A01", "speciesId": "fox"})
	if status != http.StatusUnauthorized {
		t.Fatalf("无 token adopt 状态码 = %d, 期望 401; body=%v", status, body)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Fatalf("401 响应缺少 error 字段")
	}
	// 篡改 token → 401
	status, _ = m7Do2(h, http.MethodPost, "/api/teacher/adopt", m6BadToken(token),
		map[string]any{"studentNo": "A01", "speciesId": "fox"})
	if status != http.StatusUnauthorized {
		t.Fatalf("篡改 token adopt 状态码 = %d, 期望 401", status)
	}

	// 已领养 → 409
	status, body = m7Do2(h, http.MethodPost, "/api/teacher/adopt", token,
		map[string]any{"studentNo": "A01", "speciesId": "fox"})
	if status != http.StatusConflict {
		t.Fatalf("已领养再 adopt 状态码 = %d, 期望 409", status)
	}
	if got, _ := body["error"].(string); got != "该学生已领养宠物" {
		t.Fatalf("409 error = %q, 期望原样保留", got)
	}

	// 跨班学号 → 404
	m6MustCreateStudent(t, h, teacherB, "别班", "B01")
	status, body = m7Do2(h, http.MethodPost, "/api/teacher/adopt", token,
		map[string]any{"studentNo": "B01", "speciesId": "fox"})
	if status != http.StatusNotFound {
		t.Fatalf("跨班 adopt 状态码 = %d, 期望 404", status)
	}
	if got, _ := body["error"].(string); got != "本班不存在该学号" {
		t.Fatalf("404 error = %q, 期望原样保留", got)
	}

	// studentNo 缺失 + 合法 speciesId → 400 原文案
	status, body = m7Do2(h, http.MethodPost, "/api/teacher/adopt", token,
		map[string]any{"speciesId": "fox"})
	if status != http.StatusBadRequest {
		t.Fatalf("缺 studentNo 状态码 = %d, 期望 400", status)
	}
	if got, _ := body["error"].(string); got != "studentNo 不能为空" {
		t.Fatalf("400 error = %q, 期望 studentNo 不能为空", got)
	}
	_ = s1
}

// ---------- T7：W2 前端——物种选择控件 ----------

func TestM7_W2_TeacherHTMLSpeciesPicker(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`id="adopt-species"`,
		`<option value="">随机</option>`,
		`value="cat"`, `value="dog"`, `value="bunny"`, `value="hamster"`,
		`value="chick"`, `value="penguin"`, `value="koala"`, `value="axolotl"`,
		`value="fox"`, `value="panda"`, `value="dino"`, `value="dragon"`,
		`speciesId`,
		`adopt-species`,
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少锚点 %q", anchor)
		}
	}
}

// ---------- T8：W3——发码文案 ----------

func TestM7_W3_EmailCodeCopy(t *testing.T) {
	h := newHandler(t)
	oldCopy := "验证码已发送（未配置邮件服务时见服务端日志）"
	newCopy := "验证码已发送至邮箱，请查收（10 分钟内有效）"

	for _, path := range []string{"/", "/teacher.html"} {
		body := m5Get(t, h, path)
		if strings.Contains(body, oldCopy) {
			t.Errorf("%s 仍含旧 mock 文案", path)
		}
		if n := strings.Count(body, newCopy); n != 1 {
			t.Errorf("%s 新文案出现 %d 次, 期望 1 次", path, n)
		}
	}
}
