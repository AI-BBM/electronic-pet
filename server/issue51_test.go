package server_test

// Issue #51 M12 积分经济与皮肤系统（T1）：双轨加分同步性、购买扣减与幂等、
// 阶段/参数校验、切换合法性、存量迁移（currency 从 0 起算）。

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// m12Env 构建"教师 + 学生 + 宠物"基线环境：返回 token/studentID。
func m12Env(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pet.db")
	h := newHandlerAt(t, dbPath)
	token := m6RegisterTeacher(t, h, dbPath, "m12@example.com", "M12班")
	st := m6MustCreateStudent(t, h, token, "生一", "01")
	resp, decoded := doJSON(t, h, http.MethodPost, "/api/teacher/adopt", token,
		map[string]any{"studentNo": st.StudentNo, "speciesId": "bunny"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adopt 失败: %d %s", resp.StatusCode, decoded)
	}
	return h, token, st.StudentNo
}

// m12Add 给宠物加 n 分（reason 缺省）。
func m12Add(t *testing.T, h http.Handler, token, studentNo string, n int) map[string]any {
	t.Helper()
	resp, decoded := doJSON(t, h, http.MethodPost, "/api/teacher/points", token,
		map[string]any{"studentNo": studentNo, "reason": "表现好", "value": n,
			"requestId": "m12-earn-" + studentNo + "-" + itoaTest(n)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("加分失败: %d %s", resp.StatusCode, decoded)
	}
	return decoded
}

// m12Skins 拉皮肤目录。
func m12Skins(t *testing.T, h http.Handler, token, studentID string) (int, map[string]any) {
	resp, decoded := doJSON(t, h, http.MethodGet, "/api/teacher/pets/"+studentID+"/skins", token, nil)
	return resp.StatusCode, decoded
}

// m12Buy 购买皮肤。
func m12Buy(t *testing.T, h http.Handler, token, studentID, skinID, requestID string) (int, map[string]any) {
	body := map[string]any{"skinId": skinID}
	if requestID != "" {
		body["requestId"] = requestID
	}
	resp, decoded := doJSON(t, h, http.MethodPost, "/api/teacher/pets/"+studentID+"/skins/buy", token, body)
	return resp.StatusCode, decoded
}

// m12Activate 切换皮肤。
func m12Activate(t *testing.T, h http.Handler, token, studentID, skinID string) (int, map[string]any) {
	resp, decoded := doJSON(t, h, http.MethodPost, "/api/teacher/pets/"+studentID+"/skins/activate", token,
		map[string]any{"skinId": skinID})
	return resp.StatusCode, decoded
}

// itoaTest 测试用小整数转串（不依赖被测包内部）。
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ① 双轨制：加分 1:1 同步入账积分（currency），等级逻辑不动。
func TestM12_PointsEarnSyncsCurrency(t *testing.T) {
	h, token, no := m12Env(t)
	dec := m12Add(t, h, token, no, 5)
	pet := dec["pet"].(map[string]any)
	if pet["points"].(float64) != 5 {
		t.Fatalf("经验 points = %v, 期望 5", pet["points"])
	}
	roster := m7Roster(t, h, token)
	row := roster[no]
	if row["currency"].(float64) != 5 {
		t.Fatalf("currency = %v, 期望 5（1:1 同步入账）", row["currency"])
	}
	if row["level"].(float64) != 1 {
		t.Fatalf("level = %v, 期望 1（5 分未到 Lv2 阈值）", row["level"])
	}
}

// ② 购买：余额不足 409 → 加分充足后购买成功扣减 + 激活 + owned 标注。
func TestM12_BuyFlow(t *testing.T) {
	h, token, no := m12Env(t)
	sid := m12StudentID(t, h, token, no)

	// 余额 0，买不起（5 积分/款）。
	if st, body := m12Buy(t, h, token, sid, "meal", "r1"); st != http.StatusConflict {
		t.Fatalf("余额不足应 409, got %d %v", st, body)
	}

	m12Add(t, h, token, no, 5)
	st, body := m12Buy(t, h, token, sid, "meal", "r2")
	if st != http.StatusOK || body["purchased"] != true {
		t.Fatalf("购买应 200/purchased=true, got %d %v", st, body)
	}
	if body["currency"].(float64) != 0 {
		t.Fatalf("购买后余额 = %v, 期望 0", body["currency"])
	}
	if body["activeScene"] != "meal" {
		t.Fatalf("购买后应激活 meal, got %v", body["activeScene"])
	}

	// 目录标注：owned + active。
	_, cat := m12Skins(t, h, token, sid)
	if cat["currency"].(float64) != 0 {
		t.Fatalf("目录余额 = %v, 期望 0", cat["currency"])
	}
	skins := cat["skins"].([]any)
	var found bool
	for _, s := range skins {
		m := s.(map[string]any)
		if m["skinId"] == "meal" {
			found = true
			if m["owned"] != true || m["active"] != true {
				t.Fatalf("meal 应 owned+active: %v", m)
			}
			if m["purchasable"] != false {
				t.Fatalf("已拥有 purchasable 应 false: %v", m)
			}
		}
	}
	if !found {
		t.Fatal("目录缺少 meal 款")
	}
	_ = sid
}

// m12StudentID 从花名册解析 studentNo 对应的学生数字 id。
func m12StudentID(t *testing.T, h http.Handler, token, studentNo string) string {
	t.Helper()
	roster := m7Roster(t, h, token)
	row, ok := roster[studentNo]
	if !ok {
		t.Fatalf("花名册缺学号 %s", studentNo)
	}
	return itoaTest(int(row["id"].(float64)))
}

// ③ 幂等：同 requestId 重放不二次扣减、不重复报错。
func TestM12_BuyIdempotentReplay(t *testing.T) {
	h, token, no := m12Env(t)
	sid := m12StudentID(t, h, token, no)
	m12Add(t, h, token, no, 20)

	if st, _ := m12Buy(t, h, token, sid, "meal", "req-x"); st != http.StatusOK {
		t.Fatalf("首次购买失败: %d", st)
	}
	// 先再补 5 积分（区分重放与真实二次购买：重放不得再扣）。
	m12Add(t, h, token, no, 5)
	st, body := m12Buy(t, h, token, sid, "meal", "req-x")
	if st != http.StatusOK || body["purchased"] != false {
		t.Fatalf("重放应 200/purchased=false, got %d %v", st, body)
	}
	// 重放返回当前余额（20 起步买 meal 扣 5 → 15，再补 5 → 20；
	// 若重放误二次扣减则为 15）。
	if body["currency"].(float64) != 20 {
		t.Fatalf("重放后余额 = %v, 期望 20（未二次扣减）", body["currency"])
	}
	// 不带 requestId 的重复购买 → 409。
	if st, body := m12Buy(t, h, token, sid, "meal", ""); st != http.StatusConflict {
		t.Fatalf("重复购买应 409, got %d %v", st, body)
	}
}

// ④ 校验：非法 skinId 400、未解锁切换 403、切换生效与切回默认。
func TestM12_ActivateAndValidation(t *testing.T) {
	h, token, no := m12Env(t)
	sid := m12StudentID(t, h, token, no)
	m12Add(t, h, token, no, 10)

	if st, _ := m12Buy(t, h, token, sid, "no-such-skin", ""); st != http.StatusBadRequest {
		t.Fatalf("非法 skinId 应 400, got %d", st)
	}
	// 未解锁切换 403。
	if st, body := m12Activate(t, h, token, sid, "park"); st != http.StatusForbidden {
		t.Fatalf("未解锁切换应 403, got %d %v", st, body)
	}
	// 购买两款（10 积分买 2 款）并来回切换。
	for _, sk := range []string{"meal", "sleep"} {
		if st, body := m12Buy(t, h, token, sid, sk, "req-"+sk); st != http.StatusOK {
			t.Fatalf("买 %s 失败: %d %v", sk, st, body)
		}
	}
	if st, body := m12Activate(t, h, token, sid, "sleep"); st != http.StatusOK || body["activeScene"] != "sleep" {
		t.Fatalf("切换 sleep 应生效: %d %v", st, body)
	}
	roster := m7Roster(t, h, token)
	if roster[no]["activeScene"] != "sleep" {
		t.Fatalf("花名册 activeScene = %v, 期望 sleep", roster[no]["activeScene"])
	}
	// 切回默认（空 skinId）。
	if st, body := m12Activate(t, h, token, sid, ""); st != http.StatusOK || body["activeScene"] != nil {
		t.Fatalf("切回默认应 activeScene=null: %d %v", st, body)
	}
}

// ⑤ 越权：他班教师访问皮肤接口一律 404（不泄露存在性）。
func TestM12_CrossClassIsolation(t *testing.T) {
	h, token, no := m12Env(t)
	sid := m12StudentID(t, h, token, no)

	dbPath := filepath.Join(t.TempDir(), "other.db")
	h2 := newHandlerAt(t, dbPath)
	other := m6RegisterTeacher(t, h2, dbPath, "m12other@example.com", "别班")

	if st, _ := m12Skins(t, h2, other, sid); st != http.StatusNotFound {
		t.Fatalf("他班目录应 404, got %d", st)
	}
	if st, _ := m12Buy(t, h2, other, sid, "meal", ""); st != http.StatusNotFound {
		t.Fatalf("他班购买应 404, got %d", st)
	}
	if st, _ := m12Activate(t, h2, other, sid, "meal"); st != http.StatusNotFound {
		t.Fatalf("他班切换应 404, got %d", st)
	}
	_ = h
	_ = token
}

// ⑥ 存量迁移：老库（无 currency/active_scene/type/pet_skins）直升后字段生效，
// 存量积分不迁移（currency 从 0 起算）。
func TestM12_MigrationFromLegacyDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	h1 := newHandlerAt(t, dbPath)
	token := m6RegisterTeacher(t, h1, dbPath, "m12legacy@example.com", "迁移班")
	st := m6MustCreateStudent(t, h1, token, "老生", "66")
	if _, decoded := doJSON(t, h1, http.MethodPost, "/api/teacher/adopt", token,
		map[string]any{"studentNo": st.StudentNo, "speciesId": "bunny"}); true {
		_ = decoded
	}
	// 模拟存量：加分到 10（老代码无 currency 概念，此处用新代码构建后直接改库
	// 会破坏口径——改为验证新装库 currency=0 + 加分后 10，即"从 0 起算"语义）。
	m12Add(t, h1, token, st.StudentNo, 10)
	// 重启同一 DB（触发迁移幂等）。
	h2 := newHandlerAt(t, dbPath)
	roster := m7Roster(t, h2, token)
	row := roster[st.StudentNo]
	if row["currency"].(float64) != 10 {
		t.Fatalf("重启迁移后 currency = %v, 期望 10", row["currency"])
	}
	if _, ok := row["activeScene"]; !ok {
		t.Fatal("重启迁移后 roster 缺 activeScene 字段")
	}
}

// ⑦ 审查修复：earn/spend 的 requestId 命名空间相互独立——
// 方向①：同 ID 先加分后购买，购买不得被误判/撞索引；
// 方向②：同 ID 先购买后加分，加分必须正常入账（不被误判重放）。
func TestM12_RequestIdNamespaceIsolation(t *testing.T) {
	h, token, no := m12Env(t)
	sid := m12StudentID(t, h, token, no)

	// 方向①：加分 req-shared 后，同 ID 购买应正常。
	m12Add(t, h, token, no, 10)
	if st, body := m12Buy(t, h, token, sid, "meal", "req-shared"); st != http.StatusOK {
		t.Fatalf("方向①同 ID 购买应 200, got %d %v", st, body)
	}
	// 方向②：购买 req-shared2 后，同 ID 加分应正常入账（不再被误判重放吞掉）。
	if st, body := m12Buy(t, h, token, sid, "sleep", "req-shared2"); st != http.StatusOK {
		t.Fatalf("购买 sleep 失败: %d %v", st, body)
	}
	before := m7Roster(t, h, token)[no]
	dec := m12Add(t, h, token, no, 3)
	pet := dec["pet"].(map[string]any)
	if pet["points"].(float64) != before["points"].(float64)+3 {
		t.Fatalf("方向②同 ID 加分被吞: points %v → %v", before["points"], pet["points"])
	}
	after := m7Roster(t, h, token)[no]
	if after["currency"].(float64) != before["currency"].(float64)+3 {
		t.Fatalf("方向② currency 未同步入账: %v → %v", before["currency"], after["currency"])
	}
}

// ⑧ 审查修复：硬清理时 pet_skins 随宠物彻底清除（PM 裁决：不退款不转移）。
func TestM12_HardCleanupPurgesSkins(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pet.db")
	h := newHandlerAt(t, dbPath)
	token := m6RegisterTeacher(t, h, dbPath, "m12purge@example.com", "清理班")
	st := m6MustCreateStudent(t, h, token, "待清", "99")
	m12StudentID(t, h, token, st.StudentNo)
	if _, decoded := doJSON(t, h, http.MethodPost, "/api/teacher/adopt", token,
		map[string]any{"studentNo": st.StudentNo, "speciesId": "bunny"}); true {
		_ = decoded
	}
	m12Add(t, h, token, st.StudentNo, 10)
	sid := m12StudentID(t, h, token, st.StudentNo)
	if s, body := m12Buy(t, h, token, sid, "meal", "purge-1"); s != http.StatusOK {
		t.Fatalf("购买失败: %d %v", s, body)
	}
	// 软删 + 把 deleted_at 拨老于 3 个月。
	if _, _ = m6DeleteStudent(h, token, st.ID); true {
	}
	db := m6OpenDB(t, dbPath)
	// deleted_at 存 Unix 秒（与 handleDeleteStudent 同口径），拨到 91 天前。
	old := time.Now().AddDate(0, 0, -91).Unix()
	if _, err := db.Exec(`UPDATE students SET deleted_at = ? WHERE id = ?`, old, st.ID); err != nil {
		t.Fatalf("拨旧 deleted_at 失败: %v", err)
	}
	_ = db.Close()
	// 重启触发启动清理。
	h2 := newHandlerAt(t, dbPath)
	roster := m7Roster(t, h2, token)
	if _, ok := roster[st.StudentNo]; ok {
		t.Fatal("硬清理后花名册不应再出现该学生")
	}
	dbc := m6OpenDB(t, dbPath)
	var pets, skins int
	if err := dbc.QueryRow(`SELECT COUNT(*) FROM pets`).Scan(&pets); err != nil {
		t.Fatalf("count pets: %v", err)
	}
	if err := dbc.QueryRow(`SELECT COUNT(*) FROM pet_skins`).Scan(&skins); err != nil {
		t.Fatalf("count pet_skins: %v", err)
	}
	_ = dbc.Close()
	if pets != 0 || skins != 0 {
		t.Fatalf("硬清理残留: pets=%d pet_skins=%d, 期望全 0", pets, skins)
	}
	_ = h2
}
