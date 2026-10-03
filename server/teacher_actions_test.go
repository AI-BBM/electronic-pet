package server_test

// M4 教师端黑盒用例：T4 教师代领 / T5 教师加分（含 operator 留痕）/ T7 修改教师密码。
// 契约来源：docs/product/features/m4-teacher.md + M4 测试先行任务书。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// ---------- T4 教师代领 ----------

// T4a 教师代领成功：本班未领养学生 → 200 pet（M1 形状，随机种类，points=0/level=1），
// 之后学生 GET /api/pet/me 看到同一宠物。
// 命令: go test ./server/ -run TestT4Adopt_ForStudent -v
// 预期: 实现落地前 FAIL（/api/teacher/* 未注册）；落地后 PASS。
func TestT4Adopt_ForStudent(t *testing.T) {
	h, dbPath := t4Handler(t)
	studentTok := mustJoin(t, h, "c401", "被代领生", "S001")
	teacherTok := t4TeacherToken(t, h, dbPath, "c401")

	res := t4Adopt(h, teacherTok, "S001")
	if res.Status != http.StatusOK {
		t.Fatalf("教师代领状态码 = %d, 期望 200; body=%v", res.Status, res.Body)
	}
	checkPetShape(t, res.Pet)
	if res.Pet.Level != 1 || res.Pet.Points != 0 {
		t.Errorf("代领所得 pet = (level %d, points %d), 期望 (1, 0)", res.Pet.Level, res.Pet.Points)
	}
	if res.Pet.NextLevelPoints == nil || *res.Pet.NextLevelPoints != 20 {
		t.Errorf("代领所得 nextLevelPoints = %v, 期望指向 20（Lv2 阈值不变）", res.Pet.NextLevelPoints)
	}

	// 学生侧随即可见同一宠物（同种类同名，points=0, level=1）
	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", studentTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("代领后学生 GET /api/pet/me 状态码 = %d, 期望 200; body=%v", resp.StatusCode, body)
	}
	me := petFromAny(t, body["pet"])
	if me.Species.ID != res.Pet.Species.ID || me.Name != res.Pet.Name {
		t.Errorf("学生侧宠物 = (%s/%s), 与教师代领结果 (%s/%s) 不是同一只",
			me.Species.ID, me.Name, res.Pet.Species.ID, res.Pet.Name)
	}
	if me.Points != 0 || me.Level != 1 {
		t.Errorf("学生侧宠物 = (level %d, points %d), 期望 (1, 0)", me.Level, me.Points)
	}
}

// T4b 代领错误路径：已领养 409；跨班学号 404；不存在学号 404；body 非法 400。
// 命令: go test ./server/ -run TestT4Adopt_Errors -v
// 预期: 实现落地前 FAIL；落地后 PASS。
func TestT4Adopt_Errors(t *testing.T) {
	h, dbPath := t4Handler(t)
	mustJoin(t, h, "c402", "本班已领生", "S001")
	teacherTok := t4TeacherToken(t, h, dbPath, "c402")
	if res := t4Adopt(h, teacherTok, "S001"); res.Status != http.StatusOK {
		t.Fatalf("首次代领失败: status=%d body=%v", res.Status, res.Body)
	}

	// 已领养 → 409 + error
	res := t4Adopt(h, teacherTok, "S001")
	expectError(t, res.Status, res.Body, http.StatusConflict, "重复代领已领养学生")

	// 跨班学号（S777 只存在于他班）→ 404 + error
	mustJoin(t, h, "c403", "他班生", "S777")
	res = t4Adopt(h, teacherTok, "S777")
	expectError(t, res.Status, res.Body, http.StatusNotFound, "代领他班学生")

	// 不存在的学号 → 404 + error
	res = t4Adopt(h, teacherTok, "S999")
	expectError(t, res.Status, res.Body, http.StatusNotFound, "代领不存在的学号")

	// body 非法：空学号 / 纯空白 → 400
	for i, no := range []string{"", "   "} {
		res := t4Adopt(h, teacherTok, no)
		if res.Status != http.StatusBadRequest {
			t.Errorf("空学号用例 %d (%q): 状态码 = %d, 期望 400; body=%v", i, no, res.Status, res.Body)
		}
	}
	// body 非法：非 JSON → 400
	if status, raw := t4RawRequest(h, http.MethodPost, "/api/teacher/adopt", teacherTok, "{bad"); status != http.StatusBadRequest {
		t.Errorf("非法 JSON 代领状态码 = %d, 期望 400; body=%s", status, raw)
	}
}

// ---------- T5 教师加分 ----------

// T5a 教师加分入账与 operator 留痕：响应字段正确；GET log 可见且教师加的分为
// operator=teacher、学生自加为 operator=student（倒序对照）；分页形状不变（pageSize 20）。
// 命令: go test ./server/ -run TestT4Points_OperatorTrail -v
// 预期: 实现落地前 FAIL；落地后 PASS。
func TestT4Points_OperatorTrail(t *testing.T) {
	h, dbPath := t4Handler(t)
	studentTok := mustJoin(t, h, "c501", "留痕生", "S001")
	teacherTok := t4TeacherToken(t, h, dbPath, "c501")
	if res := t4Adopt(h, teacherTok, "S001"); res.Status != http.StatusOK {
		t.Fatalf("代领失败: status=%d body=%v", res.Status, res.Body)
	}

	// 教师加 5 分（高于学生自助上限 10 以内也合法，此处验证入账与响应形状）
	res := t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S001", Reason: "教师奖励", Value: 5, RequestID: "t5-op-t"})
	if res.Status != http.StatusOK || !res.Added || res.Pet.Points != 5 || res.Level != 1 || res.LevelUp {
		t.Fatalf("教师加分 = (status=%d added=%v points=%d level=%d levelUp=%v), 期望 (200 true 5 1 false); body=%v",
			res.Status, res.Added, res.Pet.Points, res.Level, res.LevelUp, res.Body)
	}
	checkPetShape(t, res.Pet)

	// 学生自加一条对照（学生自助范围 1..10）
	if sres := m2AddPoints(h, studentTok, m2PointsRequest{Reason: "自我打卡", Value: 2, RequestID: "t5-op-s"}); sres.Status != http.StatusOK || !sres.Added {
		t.Fatalf("学生自加失败: status=%d body=%v", sres.Status, sres.Body)
	}

	// 流水：倒序（学生后加的在前）、operator 区分、分页语义不变
	list := t4GetLog(t, h, studentTok, "")
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("流水 total=%d items=%d, 期望 2/2", list.Total, len(list.Items))
	}
	if list.PageSize != 20 {
		t.Errorf("流水 pageSize = %d, 期望保持 20（分页语义不变）", list.PageSize)
	}
	if got := list.Items[0]; got.Operator != "student" || got.Value != 2 || got.Reason != "自我打卡" {
		t.Errorf("流水首条 = %+v, 期望 operator=student value=2 reason=自我打卡", got)
	}
	if got := list.Items[1]; got.Operator != "teacher" || got.Value != 5 || got.Reason != "教师奖励" {
		t.Errorf("流水第二条 = %+v, 期望 operator=teacher value=5 reason=教师奖励", got)
	}

	// 学生侧档案与教师加分结果一致（5+2=7 分，Lv1）
	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", studentTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pet/me 状态码 = %d, 期望 200; body=%v", resp.StatusCode, body)
	}
	if me := petFromAny(t, body["pet"]); me.Points != 7 || me.Level != 1 {
		t.Errorf("学生侧档案 = (level %d, points %d), 期望 (1, 7)", me.Level, me.Points)
	}
}

// T5b 参数校验：分值合法域 1..50（0/51/-1 → 400，1/50 合法）；理由 trim 后 1..100 字符。
// 命令: go test ./server/ -run TestT4Points_Validation -v
// 预期: 实现落地前 FAIL；落地后 PASS。
func TestT4Points_Validation(t *testing.T) {
	h, dbPath := t4Handler(t)
	mustJoin(t, h, "c502", "校验生", "S001")
	teacherTok := t4TeacherToken(t, h, dbPath, "c502")
	if res := t4Adopt(h, teacherTok, "S001"); res.Status != http.StatusOK {
		t.Fatalf("代领失败: status=%d body=%v", res.Status, res.Body)
	}

	longReason := strings.Repeat("优", 101)
	cases := []struct {
		name   string
		reason string
		value  int
		want   int
	}{
		{"value=0 越界", "作业优秀", 0, http.StatusBadRequest},
		{"value=51 越界", "作业优秀", 51, http.StatusBadRequest},
		{"value=-1 越界", "作业优秀", -1, http.StatusBadRequest},
		{"reason 为空", "", 1, http.StatusBadRequest},
		{"reason 纯空白", "   \t", 1, http.StatusBadRequest},
		{"reason 超 100 字符", longReason, 1, http.StatusBadRequest},
		{"合法边界 value=1", "作业优秀", 1, http.StatusOK},
		{"合法边界 value=50", "作业优秀", 50, http.StatusOK},
		{"合法 reason=100 字符", strings.Repeat("优", 100), 1, http.StatusOK},
	}
	for i, tc := range cases {
		res := t4AddPoints(h, teacherTok, t4PointsRequest{
			StudentNo: "S001", Reason: tc.reason, Value: tc.value,
			RequestID: fmt.Sprintf("t5v-%d", i),
		})
		if res.Status != tc.want {
			t.Errorf("%s: 状态码 = %d, 期望 %d; body=%v", tc.name, res.Status, tc.want, res.Body)
		}
	}
}

// T5c 目标校验与幂等：无宠物 404；跨班 404；同 requestId 重放 added=false 不重复计分。
// 命令: go test ./server/ -run TestT4Points_NotFoundAndIdempotent -v
// 预期: 实现落地前 FAIL；落地后 PASS。
func TestT4Points_NotFoundAndIdempotent(t *testing.T) {
	h, dbPath := t4Handler(t)
	mustJoin(t, h, "c503", "有宠生", "S001")
	mustJoin(t, h, "c503", "无宠生", "S002")
	teacherTok := t4TeacherToken(t, h, dbPath, "c503")
	if res := t4Adopt(h, teacherTok, "S001"); res.Status != http.StatusOK {
		t.Fatalf("代领失败: status=%d body=%v", res.Status, res.Body)
	}

	// 目标学生无宠物 → 404 + error
	res := t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S002", Reason: "作业优秀", Value: 1, RequestID: "t5n-1"})
	expectError(t, res.Status, res.Body, http.StatusNotFound, "给无宠物学生加分")

	// 跨班学生 → 404 + error
	mustJoin(t, h, "c504", "他班生", "S888")
	res = t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S888", Reason: "作业优秀", Value: 1, RequestID: "t5n-2"})
	expectError(t, res.Status, res.Body, http.StatusNotFound, "给他班学生加分")

	// requestId 幂等：同 pet 同 requestId 重放 added=false、积分/流水不变（与学生端同机制）
	first := t4PointsRequest{StudentNo: "S001", Reason: "作业优秀", Value: 7, RequestID: "t5n-dup"}
	res = t4AddPoints(h, teacherTok, first)
	if res.Status != http.StatusOK || !res.Added || res.Pet.Points != 7 {
		t.Fatalf("首次教师加分 = (status=%d added=%v points=%d), 期望 (200 true 7)", res.Status, res.Added, res.Pet.Points)
	}
	res = t4AddPoints(h, teacherTok, first)
	if res.Status != http.StatusOK || res.Added || res.Pet.Points != 7 || res.Level != 1 {
		t.Fatalf("教师加分重放 = (status=%d added=%v points=%d level=%d), 期望 (200 false 7 1); body=%v",
			res.Status, res.Added, res.Pet.Points, res.Level, res.Body)
	}
	// 同 requestId 即便参数不同也不重复计分
	res = t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S001", Reason: "课堂表现", Value: 3, RequestID: "t5n-dup"})
	if res.Added || res.Pet.Points != 7 {
		t.Errorf("同 requestId 异参重放 = (added=%v points=%d), 期望 (false 7)", res.Added, res.Pet.Points)
	}
	// 不同 requestId 正常计分
	res = t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S001", Reason: "进步之星", Value: 1, RequestID: "t5n-new"})
	if !res.Added || res.Pet.Points != 8 {
		t.Errorf("新 requestId = (added=%v points=%d), 期望 (true 8)", res.Added, res.Pet.Points)
	}
}

// T5d 跨级：教师 19+1 → Lv2（阈值 20/60 不变），跨级表现与学生端一致；
// 教师单次 19 分同时验证教师额度（高于学生自助 1..10）。
// 命令: go test ./server/ -run TestT4Points_CrossesLv2 -v
// 预期: 实现落地前 FAIL；落地后 PASS。
func TestT4Points_CrossesLv2(t *testing.T) {
	h, dbPath := t4Handler(t)
	studentTok := mustJoin(t, h, "c505", "跨级生", "S001")
	teacherTok := t4TeacherToken(t, h, dbPath, "c505")
	if res := t4Adopt(h, teacherTok, "S001"); res.Status != http.StatusOK {
		t.Fatalf("代领失败: status=%d body=%v", res.Status, res.Body)
	}

	// 教师 +19（单次即可，超过学生上限 10）：尚不足 Lv2
	res := t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S001", Reason: "阶段奖励", Value: 19, RequestID: "t5c-a"})
	if res.Status != http.StatusOK || res.LevelUp || res.Level != 1 || res.Pet.Points != 19 {
		t.Fatalf("教师 +19 = (levelUp=%v level=%d points=%d), 期望 (false 1 19); body=%v",
			res.LevelUp, res.Level, res.Pet.Points, res.Body)
	}
	if res.Pet.NextLevelPoints == nil || *res.Pet.NextLevelPoints != 20 {
		t.Errorf("+19 后 nextLevelPoints = %v, 期望指向 20", res.Pet.NextLevelPoints)
	}
	// 再 +1 跨级
	res = t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S001", Reason: "临门一分", Value: 1, RequestID: "t5c-b"})
	if res.Status != http.StatusOK || !res.Added || !res.LevelUp || res.Level != 2 || res.Pet.Points != 20 {
		t.Fatalf("19+1 = (added=%v levelUp=%v level=%d points=%d), 期望 (true true 2 20); body=%v",
			res.Added, res.LevelUp, res.Level, res.Pet.Points, res.Body)
	}
	if res.Pet.NextLevelPoints == nil || *res.Pet.NextLevelPoints != 60 {
		t.Errorf("Lv2 后 nextLevelPoints = %v, 期望指向 60", res.Pet.NextLevelPoints)
	}

	// 学生侧档案与加分响应立即一致
	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", studentTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pet/me 状态码 = %d, 期望 200; body=%v", resp.StatusCode, body)
	}
	if me := petFromAny(t, body["pet"]); me.Points != 20 || me.Level != 2 {
		t.Errorf("学生侧档案 = (level %d, points %d), 期望 (2, 20)", me.Level, me.Points)
	}
}

// ---------- T7 修改教师密码 ----------

// T7 改密：旧密错 403；新密 trim 后空/超 32 字符 400；正常改 200 后旧密 401、新密 200。
// 命令: go test ./server/ -run TestT4Passcode -v
// 预期: 实现落地前 FAIL；落地后 PASS。
func TestT4Passcode(t *testing.T) {
	h, dbPath := t4Handler(t)
	mustJoin(t, h, "c701", "改密班学生", "S001")
	t4SetPasscode(t, dbPath, "c701", "旧密码-初始")
	teacherTok := t4MustLogin(t, h, "c701", "旧密码-初始")

	// 旧密码错 → 403 + error，且密码未被改动（原密码仍可登录）
	status, body := t4Passcode(h, teacherTok, "wrong-old", "新密码-A")
	expectError(t, status, body, http.StatusForbidden, "旧密码错误的改密")
	if res := t4Login(h, "c701", "旧密码-初始"); res.Status != http.StatusOK {
		t.Errorf("改密被拒后原密码登录状态码 = %d, 期望 200（密码未变）; body=%v", res.Status, res.Body)
	}

	// 新密码非法：trim 后空 / 超 32 字符 → 400
	for i, np := range []string{"", "   ", strings.Repeat("p", 33)} {
		status, body := t4Passcode(h, teacherTok, "旧密码-初始", np)
		if status != http.StatusBadRequest {
			t.Errorf("非法新密码用例 %d (%q): 状态码 = %d, 期望 400; body=%v", i, np, status, body)
		}
	}

	// 正常改密（新密码取 32 字符上边界，合法）→ 200；旧密码登录 401、新密码登录 200
	newPass := strings.Repeat("n", 32)
	status, body = t4Passcode(h, teacherTok, "旧密码-初始", newPass)
	if status != http.StatusOK {
		t.Fatalf("正常改密状态码 = %d, 期望 200; body=%v", status, body)
	}
	if res := t4Login(h, "c701", "旧密码-初始"); res.Status != http.StatusUnauthorized {
		t.Errorf("改密后旧密码登录状态码 = %d, 期望 401; body=%v", res.Status, res.Body)
	}
	newTok := t4MustLogin(t, h, "c701", newPass)
	if roster := t4Roster(h, newTok); roster.Status != http.StatusOK {
		t.Errorf("新密码登录所得 token 调 roster 状态码 = %d, 期望 200; body=%v", roster.Status, roster.Body)
	}
}
