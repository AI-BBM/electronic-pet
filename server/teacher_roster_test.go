package server_test

// M4 教师端黑盒用例：T6 班级花名册（只读视图）。
// 契约来源：docs/product/features/m4-teacher.md + M4 测试先行任务书。

import (
	"net/http"
	"testing"
)

// T6 花名册：混合班（教师代领+教师加分 / 学生自领无流水 / 未领养）与实际一致；
// 按 studentNo 升序；未领养字段形态（adopted=false、species/lastPointsAt 空串、
// level/points=0）；已领养 species 与学生侧 petJSON 同源；lastPointsAt=最近一条流水
// createdAt（无流水为空串）；只含本班学生；条目携带契约规定的全部字段。
// 命令: go test ./server/ -run TestT4Roster -v
// 预期: 实现落地前 FAIL（/api/teacher/roster 未注册）；落地后 PASS。
func TestT4Roster(t *testing.T) {
	h, dbPath := t4Handler(t)
	s1 := mustJoin(t, h, "c601", "甲一", "S001")
	mustJoin(t, h, "c601", "乙二", "S002")
	mustJoin(t, h, "c601", "丙三", "S003")
	teacherTok := t4TeacherToken(t, h, dbPath, "c601")

	// S001：教师代领 + 教师加分 5（有流水）；S002：学生自领（无流水）；S003：未领养
	if res := t4Adopt(h, teacherTok, "S001"); res.Status != http.StatusOK {
		t.Fatalf("S001 教师代领失败: status=%d body=%v", res.Status, res.Body)
	}
	if res := t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S001", Reason: "教师奖励", Value: 5, RequestID: "t6-1"}); res.Status != http.StatusOK || !res.Added {
		t.Fatalf("S001 教师加分失败: status=%d body=%v", res.Status, res.Body)
	}
	s2 := mustJoin(t, h, "c601", "乙二", "S002") // 幂等找回，取 S002 学生 token
	mustAdopt(t, h, s2, "random")

	// 他班学生（S901）：不得出现在本班花名册
	mustJoin(t, h, "c602", "他班一", "S901")

	res := t4Roster(h, teacherTok)
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/teacher/roster 状态码 = %d, 期望 200; body=%v", res.Status, res.Body)
	}
	if len(res.Students) != 3 {
		t.Fatalf("花名册人数 = %d, 期望 3（只含本班）: %+v", len(res.Students), res.Students)
	}

	// 按 studentNo 升序
	if res.Students[0].StudentNo != "S001" || res.Students[1].StudentNo != "S002" || res.Students[2].StudentNo != "S003" {
		t.Errorf("花名册顺序 = %s,%s,%s, 期望按 studentNo 升序",
			res.Students[0].StudentNo, res.Students[1].StudentNo, res.Students[2].StudentNo)
	}

	// 不含他班学生（显式复核）
	for _, s := range res.Students {
		if s.StudentNo == "S901" {
			t.Errorf("花名册混入他班学生 S901: %+v", s)
		}
	}

	// S001：已领养，species 与学生侧 petJSON 同源，分值/等级与实际一致
	resp, meBody := doJSON(t, h, http.MethodGet, "/api/pet/me", s1, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("S001 GET /api/pet/me 状态码 = %d, 期望 200; body=%v", resp.StatusCode, meBody)
	}
	me := petFromAny(t, meBody["pet"])
	r1 := res.Students[0]
	if !r1.Adopted || r1.SpeciesID != me.Species.ID || r1.SpeciesName != me.Species.Name {
		t.Errorf("S001 花名册 species = (%s/%s), 与学生侧 (%s/%s) 不同源; entry=%+v",
			r1.SpeciesID, r1.SpeciesName, me.Species.ID, me.Species.Name, r1)
	}
	if r1.Name != "甲一" || r1.Level != me.Level || r1.Points != me.Points {
		t.Errorf("S001 花名册 = (name %q level %d points %d), 期望 (甲一 %d %d)", r1.Name, r1.Level, r1.Points, me.Level, me.Points)
	}
	if r1.Points != 5 || r1.Level != 1 {
		t.Errorf("S001 花名册 = (level %d, points %d), 期望 (1, 5)", r1.Level, r1.Points)
	}
	// lastPointsAt = 该宠物最近一条流水 createdAt
	log := t4GetLog(t, h, s1, "")
	if log.Total != 1 || len(log.Items) != 1 || log.Items[0].CreatedAt == "" {
		t.Fatalf("S001 流水异常: total=%d items=%d", log.Total, len(log.Items))
	}
	if r1.LastPointsAt != log.Items[0].CreatedAt {
		t.Errorf("S001 lastPointsAt = %q, 期望等于最近流水 createdAt %q", r1.LastPointsAt, log.Items[0].CreatedAt)
	}

	// S002：已领养但无流水 → lastPointsAt 为空串，其余字段来自宠物
	r2 := res.Students[1]
	if !r2.Adopted || r2.SpeciesID == "" || r2.SpeciesName == "" {
		t.Errorf("S002 花名册 = %+v, 期望 adopted=true 且 speciesId/speciesName 非空", r2)
	}
	if r2.Name != "乙二" || r2.Level != 1 || r2.Points != 0 {
		t.Errorf("S002 花名册 = (name %q level %d points %d), 期望 (乙二 1 0)", r2.Name, r2.Level, r2.Points)
	}
	if r2.LastPointsAt != "" {
		t.Errorf("S002 lastPointsAt = %q, 期望空串（无流水）", r2.LastPointsAt)
	}

	// S003：未领养形态
	r3 := res.Students[2]
	if r3.Adopted {
		t.Errorf("S003 adopted = true, 期望 false（未领养）")
	}
	if r3.Name != "丙三" {
		t.Errorf("S003 花名册 name = %q, 期望 丙三", r3.Name)
	}
	if r3.SpeciesID != "" || r3.SpeciesName != "" || r3.LastPointsAt != "" {
		t.Errorf("S003 未领养字段形态 = %+v, 期望 speciesId/speciesName/lastPointsAt 均为空串", r3)
	}
	if r3.Level != 0 || r3.Points != 0 {
		t.Errorf("S003 花名册 = (level %d, points %d), 期望 (0, 0)", r3.Level, r3.Points)
	}

	// 字段完整性：花名册条目必须显式携带契约规定的全部 8 个字段
	rawStudents, ok := res.Body["students"].([]any)
	if !ok || len(rawStudents) != 3 {
		t.Fatalf("花名册原始 students 非法: %v", res.Body["students"])
	}
	rawThird, ok := rawStudents[2].(map[string]any)
	if !ok {
		t.Fatalf("花名册第 3 条不是 JSON 对象: %v", rawStudents[2])
	}
	for _, key := range []string{"studentNo", "name", "adopted", "speciesId", "speciesName", "level", "points", "lastPointsAt"} {
		if _, ok := rawThird[key]; !ok {
			t.Errorf("花名册条目缺少字段 %q: %v", key, rawThird)
		}
	}
}
