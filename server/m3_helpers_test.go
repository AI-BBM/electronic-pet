package server_test

// M3 测试公共设施：黑盒风格，全部走真实 API（join/adopt/points）造数据，
// 复用 M1/M2 的 helpers（newHandler/mustJoin/adoptEgg/m2AddPoints 等），
// 命名统一加 m3 前缀避免与 M1/M2 helper 冲突。
//
// 契约来源：docs/product/features/m3-dex-wall.md + M3 测试先行任务书。
// 注意：adopt 的种类是服务端随机加权的，任何断言都不得写死「某学生孵出某种类」，
// 只能以「adopt 响应返回的 speciesId」为锚点做确定性交叉断言。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// m3DexEntry 是 GET /api/dex 中单个种类条目的类型化字段集。
// Stages 未解锁时必须为 JSON null（解码为 nil 切片）。
type m3DexEntry struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Rarity     string   `json:"rarity"`
	Unlocked   bool     `json:"unlocked"`
	Owners     int      `json:"owners"`
	Stages     []string `json:"stages"`
	Silhouette string   `json:"silhouette"`
}

// m3DexResp 是 GET /api/dex 的响应体形状。
type m3DexResp struct {
	Species []m3DexEntry `json:"species"`
}

// m3WallEntry 是班级墙单个条目的类型化字段集（LatestActivity 无流水时为 null）。
type m3WallEntry struct {
	PetID          int64   `json:"petId"`
	PetName        string  `json:"petName"`
	StudentName    string  `json:"studentName"`
	SpeciesID      string  `json:"speciesId"`
	SpeciesName    string  `json:"speciesName"`
	Rarity         string  `json:"rarity"`
	Level          int     `json:"level"`
	Points         int     `json:"points"`
	ImageURL       string  `json:"imageUrl"`
	LatestReason   string  `json:"latestReason"`
	LatestActivity *string `json:"latestActivity"`
}

// m3WallResp 是 GET /api/class/wall 的响应体形状（Wall 空班时为 [] 非 null）。
type m3WallResp struct {
	Sort string        `json:"sort"`
	Wall []m3WallEntry `json:"wall"`
}

// m3AdoptedPet 是 adopt 响应中 pet 的最小字段集（含 wall 对账需要的 id）。
type m3AdoptedPet struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Species petSpecies `json:"species"`
	Level   int        `json:"level"`
	Points  int        `json:"points"`
}

// m3CanonicalSpeciesIDs 是 canonical 物种定稿表的全部 id（12 种），
// 来源 tools/assets/species.meta.json（1d00e4c）。dex 必须恰好覆盖这 12 个 id；
// 但「哪个学生孵出哪个种类」是随机的，断言绝不写死。
var m3CanonicalSpeciesIDs = []string{
	"cat", "dog", "bunny", "hamster", "chick", "penguin",
	"koala", "axolotl", "fox", "panda", "dino", "dragon",
}

// m3Get 发送 GET 请求并返回状态码与原始响应体（不要求响应是 JSON，
// 用于 401 探测、SPA 回退 HTML 等场景；仅测试 goroutine 使用）。
func m3Get(h http.Handler, target, token string) (int, []byte) {
	resp := performJSON(h, http.MethodGet, target, token, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// m3DecodeJSON 把响应体解码为 JSON 对象；不是 JSON 对象时返回 nil，
// 由调用方决定如何失败（保留原始 body 供报错）。
func m3DecodeJSON(raw []byte) map[string]any {
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return body
}

// m3GetDex 调 GET /api/dex：返回状态码、类型化响应与原始 body（仅测试 goroutine）。
func m3GetDex(t *testing.T, h http.Handler, token string) (int, m3DexResp, []byte) {
	t.Helper()
	status, raw := m3Get(h, "/api/dex", token)
	var resp m3DexResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("GET /api/dex 响应不是 JSON: %s", raw)
	}
	return status, resp, raw
}

// m3GetWall 调 GET /api/class/wall（query 形如 "?sort=recent" 或 ""；仅测试 goroutine）。
func m3GetWall(t *testing.T, h http.Handler, token, query string) (int, m3WallResp, []byte) {
	t.Helper()
	status, raw := m3Get(h, "/api/class/wall"+query, token)
	var resp m3WallResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("GET /api/class/wall%s 响应不是 JSON: %s", query, raw)
	}
	return status, resp, raw
}

// m3Adopt 用 random 蛋孵化并返回含 pet id 的宠物快照（adopt 种类由服务端随机决定）。
func m3Adopt(t *testing.T, h http.Handler, token string) m3AdoptedPet {
	t.Helper()
	status, raw := adoptEgg(h, token, "random")
	if status != http.StatusOK {
		t.Fatalf("adopt(random) 状态码 = %d, 期望 200, body=%s", status, raw)
	}
	var env struct {
		Pet m3AdoptedPet `json:"pet"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("adopt 响应解码失败 (%s): %v", raw, err)
	}
	if env.Pet.ID == 0 || env.Pet.Species.ID == "" {
		t.Fatalf("adopt 响应缺少 pet.id 或 species.id: %s", raw)
	}
	return env.Pet
}

// m3ExpectUnauthorized 断言目标端点在「无 token」与「篡改 token」两种情况下
// 均返回 401 且携带非空 {"error":"..."}（复用 M2 的 m2TamperToken）。
func m3ExpectUnauthorized(t *testing.T, h http.Handler, target, validToken string) {
	t.Helper()
	cases := []struct{ name, token string }{
		{"无 token", ""},
		{"篡改 token", m2TamperToken(validToken)},
	}
	for _, c := range cases {
		status, raw := m3Get(h, target, c.token)
		expectError(t, status, m3DecodeJSON(raw), http.StatusUnauthorized, "GET "+target+"（"+c.name+"）")
	}
}

// m3DexEntryMaps 把 dex 响应逐条解码为 map（保留 null / 缺键语义，用于红线检查）。
func m3DexEntryMaps(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	body := m3DecodeJSON(raw)
	if body == nil {
		t.Fatalf("dex 响应不是 JSON 对象: %s", raw)
	}
	arr, ok := body["species"].([]any)
	if !ok {
		t.Fatalf("dex 响应缺少 species 数组: %s", raw)
	}
	entries := make([]map[string]any, 0, len(arr))
	for i, v := range arr {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("dex 第 %d 条不是 JSON 对象: %v", i, v)
		}
		entries = append(entries, m)
	}
	return entries
}

// m3WallEntryMaps 把班级墙响应逐条解码为 map（用于键存在性检查，如 studentNo 不得出现）。
func m3WallEntryMaps(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	body := m3DecodeJSON(raw)
	if body == nil {
		t.Fatalf("wall 响应不是 JSON 对象: %s", raw)
	}
	arr, ok := body["wall"].([]any)
	if !ok {
		t.Fatalf("wall 响应缺少 wall 数组（可能为 null）: %s", raw)
	}
	entries := make([]map[string]any, 0, len(arr))
	for i, v := range arr {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("wall 第 %d 条不是 JSON 对象: %v", i, v)
		}
		entries = append(entries, m)
	}
	return entries
}

// m3CheckDexShape 校验 dex 整体形状：恰好 12 条、id 唯一且覆盖 canonical 全部 id、
// name/rarity/silhouette 非空、rarity 合法且分布为 8 common / 3 rare / 1 epic。
// 返回 id → 条目的索引便于交叉断言。
func m3CheckDexShape(t *testing.T, resp m3DexResp) map[string]m3DexEntry {
	t.Helper()
	if len(resp.Species) != 12 {
		t.Fatalf("dex 条数 = %d, 期望恰好 12", len(resp.Species))
	}
	byID := make(map[string]m3DexEntry, 12)
	rarityCount := map[string]int{}
	for _, e := range resp.Species {
		if e.ID == "" {
			t.Errorf("dex 存在 id 为空的条目")
			continue
		}
		if _, dup := byID[e.ID]; dup {
			t.Errorf("dex id 重复: %s", e.ID)
		}
		byID[e.ID] = e
		if e.Name == "" {
			t.Errorf("dex[%s]: name 为空", e.ID)
		}
		if !isValidRarity(e.Rarity) {
			t.Errorf("dex[%s]: rarity = %q, 期望 common/rare/epic 之一", e.ID, e.Rarity)
		} else {
			rarityCount[e.Rarity]++
		}
		if e.Silhouette == "" {
			t.Errorf("dex[%s]: silhouette 为空", e.ID)
		}
	}
	for _, id := range m3CanonicalSpeciesIDs {
		if _, ok := byID[id]; !ok {
			t.Errorf("dex 缺少 canonical 种类 %s", id)
		}
	}
	if rarityCount["common"] != 8 || rarityCount["rare"] != 3 || rarityCount["epic"] != 1 {
		t.Errorf("稀有度分布 = common:%d rare:%d epic:%d, 期望 8/3/1",
			rarityCount["common"], rarityCount["rare"], rarityCount["epic"])
	}
	return byID
}

// m3CheckUnlockedEntry 校验已解锁条目：unlocked=true、owners>=1、
// stages 为长度 3 且全部非空 URL 的数组。
func m3CheckUnlockedEntry(t *testing.T, e m3DexEntry) {
	t.Helper()
	if !e.Unlocked {
		t.Errorf("dex[%s]: unlocked = false, 期望 true", e.ID)
	}
	if e.Owners < 1 {
		t.Errorf("dex[%s]: owners = %d, 期望 >= 1", e.ID, e.Owners)
	}
	if len(e.Stages) != 3 {
		t.Errorf("dex[%s]: stages 长度 = %d, 期望 3（%v）", e.ID, len(e.Stages), e.Stages)
	}
	for i, u := range e.Stages {
		if u == "" {
			t.Errorf("dex[%s]: stages[%d] 为空 URL", e.ID, i)
		}
	}
}

// m3CheckLockedEntry 校验单个 locked 条目（map 语义）：unlocked 键存在且为 false、
// owners 键存在且为 0、stages 键存在且值为 null；且整个条目重新序列化后
// 不含 "stages":[（验收红线：未解锁不得泄露彩色立绘 URL）。
func m3CheckLockedEntry(t *testing.T, entry map[string]any) {
	t.Helper()
	id, _ := entry["id"].(string)
	if v, ok := entry["unlocked"]; !ok {
		t.Errorf("dex[%s]: 缺少 unlocked 键", id)
	} else if b, _ := v.(bool); b {
		t.Errorf("dex[%s]: unlocked = true, 期望 false", id)
	}
	if v, ok := entry["owners"]; !ok {
		t.Errorf("dex[%s]: 缺少 owners 键", id)
	} else if n, _ := v.(float64); n != 0 {
		t.Errorf("dex[%s]: owners = %v, 期望 0", id, v)
	}
	if v, ok := entry["stages"]; !ok {
		t.Errorf("dex[%s]: 缺少 stages 键, 期望显式 null", id)
	} else if v != nil {
		t.Errorf("dex[%s]: stages = %v, 期望 null（未解锁不得泄露立绘 URL）", id, v)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("重新编码 dex[%s] 失败: %v", id, err)
	}
	if bytes.Contains(raw, []byte(`"stages":[`)) {
		t.Errorf("dex[%s]: 序列化后泄露 stages 数组: %s", id, raw)
	}
}

// m3CheckWallEntryKeys 校验 wall 条目的键集合：latestReason 键存在且为 ""、
// latestActivity 键存在且为 null、不得包含 studentNo / student_no（学号是身份键，不上墙）。
func m3CheckWallEntryKeys(t *testing.T, entry map[string]any, context string) {
	t.Helper()
	if v, ok := entry["latestReason"]; !ok {
		t.Errorf("%s: 缺少 latestReason 键, 期望显式空串", context)
	} else if s, _ := v.(string); s != "" {
		t.Errorf("%s: latestReason = %q, 期望 \"\"", context, s)
	}
	if v, ok := entry["latestActivity"]; !ok {
		t.Errorf("%s: 缺少 latestActivity 键, 期望显式 null", context)
	} else if v != nil {
		t.Errorf("%s: latestActivity = %v, 期望 null（无流水）", context, v)
	}
	for _, forbidden := range []string{"studentNo", "student_no"} {
		if _, ok := entry[forbidden]; ok {
			t.Errorf("%s: 条目包含隐私键 %s（学号不得上墙）", context, forbidden)
		}
	}
}
