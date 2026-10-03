package server_test

// M6 纯教师侧改造（issue #25）测试先行用例。
// 契约来源：docs/product/features/m6-teacher-only.md + M6 测试先行任务书（实现按此钉死）。
//
// 覆盖（编号与任务书一致）：
//   T1  发码（限速钉死 429 / meta 键与 6 位数字）
//   T2  注册全链路（错码/过期码/重复邮箱/密码长度/className 空）
//   T3  邮箱式登录
//   T4  名单增（重复学号 409 / 空白 400）
//   T5  名单改（撞在册 409 / 新学号生效 / 不存在与已删除 404）
//   T6  名单删与垃圾桶（roster 过滤 / trash 形状 / 再删 404 / 宠物流水保留）
//   T7  恢复（无冲突 200 / 学号被占 409 / 不存在 404）
//   T8  无 token 与篡改 token 打教师端点 → 401（学生式 token 无法合法签发，见 T11）
//   T9  教师改宠物名（可多次 / 不存在学生 / 无宠物 / 空白与超长 400）
//   T10 学生端点下线 404（GET 与 POST 逐个；含 teacher/passcode）
//   T11 学生视角终局确认——Skip（无法获得学生 token，语义由 T8 覆盖）
//   T12 roster 只含在册
//   T13 M5 老库重建迁移（数据无损 / deleted_at / 部分唯一索引 / 旧 UNIQUE 消失）
//   T14 迁移幂等 + 口令式登录下线 + 教师 token 跨重启
//   T15 迁移后老学生的垃圾桶恢复唯一性
//   T16 清理任务（New() 启动执行一次；100 天硬删 / 89 天保留）
//   T17 教师前端契约（四标记 + viewport + style.css 不回退）
//   T18 方法级 405（沿用 M2 显式注册模式）
//   T19 沿用端点回归（teacher/adopt、teacher/points、roster 行为不变）
//
// 预期红态：实现落地前全部 FAIL（端点未注册 / 列未建 / 默认页未切换）。

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- T1 发码 ----------

// T1 发码：合法邮箱 200 {ok:true}；非法邮箱 400；同邮箱 60s 内重发钉死 429；
// mock 模式下 meta 表有 email_code:<email> 且码为 6 位数字（值格式 <code>|<expiresUnix>）。
// 命令: go test ./server/ -run TestM6_T1_EmailCode -v
func TestM6_T1_EmailCode(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "t1@example.com"

	// 非法邮箱 → 400 + error（先测，避免消耗 t1@ 的 60s 发码窗口）
	for _, bad := range []string{"", "not-an-email", "a b@example.com", "abc@"} {
		status, body := m6EmailCode(h, bad)
		expectError(t, status, body, http.StatusBadRequest, fmt.Sprintf("非法邮箱 %q 发码", bad))
	}

	// 合法邮箱 → 200 + {ok:true}
	status, body := m6EmailCode(h, email)
	if status != http.StatusOK {
		t.Fatalf("发码(%s) 状态码 = %d, 期望 200; body=%v", email, status, body)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Errorf("发码响应 = %v, 期望携带 ok:true", body)
	}

	// mock 模式落库：键 email_code:<email>，码 6 位数字（m6ReadEmailCode 内含断言）
	m6ReadEmailCode(t, dbPath, email)

	// 同邮箱 60s 内重发 → 429（钉死值，任务书允许 429/400 二选一，本套件钉 429）
	status, body = m6EmailCode(h, email)
	expectError(t, status, body, http.StatusTooManyRequests, "同邮箱 60s 内重发")
}

// ---------- T2 注册 ----------

// T2 注册：发码 → 直查 DB 取码 → 注册 200 {token}；classes 出现新班、teachers 有该邮箱；
// 同邮箱再注册 409；错码 400；过期码（直改 DB 过期时间）400；密码 7 字符 400；className 空 400。
// 命令: go test ./server/ -run TestM6_T2_Register -v
func TestM6_T2_Register(t *testing.T) {
	h, dbPath := m6Handler(t)

	// 错码 → 400（用独立邮箱，避免与成功路径相互影响码的有效性）
	emailWrong := "t2-wrong@example.com"
	if status, body := m6EmailCode(h, emailWrong); status != http.StatusOK {
		t.Fatalf("发码(%s) 状态码 = %d, 期望 200; body=%v", emailWrong, status, body)
	}
	wrongCode := m6WrongCode(m6ReadEmailCode(t, dbPath, emailWrong))
	status, body := m6Register(h, emailWrong, wrongCode, m6Password, "错码班")
	expectError(t, status, body, http.StatusBadRequest, "错码注册")

	// 正确注册 → 200 {token}，token 可调教师端点
	email := "t2@example.com"
	if status, body := m6EmailCode(h, email); status != http.StatusOK {
		t.Fatalf("发码(%s) 状态码 = %d, 期望 200; body=%v", email, status, body)
	}
	code := m6ReadEmailCode(t, dbPath, email)
	status, body = m6Register(h, email, code, m6Password, "二年三班")
	if status != http.StatusOK {
		t.Fatalf("注册(%s) 状态码 = %d, 期望 200; body=%v", email, status, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("注册(%s) 未返回 token: %v", email, body)
	}
	if rs, rbody, _ := m6Roster(h, token); rs != http.StatusOK {
		t.Fatalf("注册所得 token 调 roster 状态码 = %d, 期望 200; body=%v", rs, rbody)
	}

	// DB 断言：teachers 有该邮箱，class_id 关联一个真实班级且班级码非空
	db := m6OpenDB(t, dbPath)
	var classID int64
	if err := db.QueryRow(`SELECT class_id FROM teachers WHERE email = ?`, email).Scan(&classID); err != nil {
		t.Fatalf("teachers 表缺少 %s（注册必须落 teachers 行）: %v", email, err)
	}
	var classCode string
	if err := db.QueryRow(`SELECT code FROM classes WHERE id = ?`, classID).Scan(&classCode); err != nil {
		t.Fatalf("注册建的班级 %d 不存在: %v", classID, err)
	}
	if classCode == "" {
		t.Errorf("注册建的班级 code 为空串, 期望自动生成的非空班级码")
	}

	// 同邮箱再注册 → 409。发码接口受 60s 限速无法为已注册邮箱重取新码，
	// 故按约定格式直写一条「码正确且未过期」的记录，保证冲突检测发生在邮箱维度。
	m6WriteEmailCode(t, dbPath, email, "654321", time.Now().Add(10*time.Minute))
	status, body = m6Register(h, email, "654321", m6Password, "重复班")
	expectError(t, status, body, http.StatusConflict, "同邮箱重复注册")

	// 过期码 → 400（直改 DB 把 expires 挪到过去，码本身保持正确）
	emailExp := "t2-exp@example.com"
	if status, body := m6EmailCode(h, emailExp); status != http.StatusOK {
		t.Fatalf("发码(%s) 状态码 = %d, 期望 200; body=%v", emailExp, status, body)
	}
	codeExp := m6ReadEmailCode(t, dbPath, emailExp)
	m6ExpireEmailCode(t, dbPath, emailExp, time.Now().Add(-time.Minute))
	status, body = m6Register(h, emailExp, codeExp, m6Password, "过期班")
	expectError(t, status, body, http.StatusBadRequest, "过期码注册")

	// 密码 7 字符 → 400
	emailPwd := "t2-pwd@example.com"
	if status, body := m6EmailCode(h, emailPwd); status != http.StatusOK {
		t.Fatalf("发码(%s) 状态码 = %d, 期望 200; body=%v", emailPwd, status, body)
	}
	codePwd := m6ReadEmailCode(t, dbPath, emailPwd)
	status, body = m6Register(h, emailPwd, codePwd, "1234567", "短密码班")
	expectError(t, status, body, http.StatusBadRequest, "7 字符密码注册")

	// className 空 → 400
	emailCls := "t2-cls@example.com"
	if status, body := m6EmailCode(h, emailCls); status != http.StatusOK {
		t.Fatalf("发码(%s) 状态码 = %d, 期望 200; body=%v", emailCls, status, body)
	}
	codeCls := m6ReadEmailCode(t, dbPath, emailCls)
	status, body = m6Register(h, emailCls, codeCls, m6Password, "")
	expectError(t, status, body, http.StatusBadRequest, "className 空注册")
}

// ---------- T3 登录 ----------

// T3 登录：注册后邮箱密码登录 200 {token} 且可调 roster；密码错 401；邮箱不存在 401。
// 命令: go test ./server/ -run TestM6_T3_Login -v
func TestM6_T3_Login(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "t3@example.com"
	m6RegisterTeacher(t, h, dbPath, email, "三年一班")

	status, body := m6Login(h, email, m6Password)
	if status != http.StatusOK {
		t.Fatalf("登录(%s) 状态码 = %d, 期望 200; body=%v", email, status, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("登录(%s) 未返回 token: %v", email, body)
	}
	if rs, rbody, _ := m6Roster(h, token); rs != http.StatusOK {
		t.Fatalf("登录所得 token 调 roster 状态码 = %d, 期望 200; body=%v", rs, rbody)
	}

	status, body = m6Login(h, email, "definitely-wrong")
	expectError(t, status, body, http.StatusUnauthorized, "密码错登录")

	status, body = m6Login(h, "nobody@example.com", m6Password)
	expectError(t, status, body, http.StatusUnauthorized, "邮箱不存在登录")
}

// ---------- T4 名单增 ----------

// T4 名单增：200 {student:{id,name,studentNo}}；同班在册学号重复 409；姓名/学号空白 400。
// 命令: go test ./server/ -run TestM6_T4_CreateStudent -v
func TestM6_T4_CreateStudent(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t4@example.com", "四年一班")

	m6MustCreateStudent(t, h, token, "张小测", "01")

	// 同班同学号重复 → 409
	status, st, body := m6CreateStudent(h, token, "李重复", "01")
	if status != http.StatusConflict {
		t.Errorf("重复学号状态码 = %d, 期望 409; body=%v", status, body)
	} else if st.ID != 0 {
		t.Errorf("重复学号响应携带了 student: %+v", st)
	}

	// 空白字段 → 400
	for i, tc := range []struct{ name, no string }{
		{"", "02"}, {"   ", "02"}, {"王空白", ""}, {"王空白", "   "},
	} {
		status, _, body := m6CreateStudent(h, token, tc.name, tc.no)
		expectError(t, status, body, http.StatusBadRequest,
			fmt.Sprintf("空白字段用例 %d (%q,%q)", i, tc.name, tc.no))
	}
}

// ---------- T5 名单改 ----------

// T5 名单改：改姓名 200 生效；改学号撞在册 409；改到新学号 200 且 roster 生效；
// 不存在 404；已删除（垃圾桶内）404。
// 命令: go test ./server/ -run TestM6_T5_PatchStudent -v
func TestM6_T5_PatchStudent(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t5@example.com", "五年一班")
	a := m6MustCreateStudent(t, h, token, "王改名", "01")
	m6MustCreateStudent(t, h, token, "李在册", "02")

	// 改姓名 → 200，学号不变
	status, st, body := m6PatchStudent(h, token, a.ID, map[string]string{"name": "王新名"})
	if status != http.StatusOK {
		t.Fatalf("改姓名状态码 = %d, 期望 200; body=%v", status, body)
	}
	if st.Name != "王新名" || st.StudentNo != "01" {
		t.Errorf("改姓名响应 = %+v, 期望 name=王新名 studentNo=01", st)
	}

	// 改学号撞在册 → 409
	status, _, body = m6PatchStudent(h, token, a.ID, map[string]string{"studentNo": "02"})
	expectError(t, status, body, http.StatusConflict, "改学号撞在册")

	// 改到新学号 → 200，roster 反映
	status, st, body = m6PatchStudent(h, token, a.ID, map[string]string{"studentNo": "011"})
	if status != http.StatusOK || st.StudentNo != "011" {
		t.Fatalf("改新学号 = (status=%d student=%+v), 期望 (200 011); body=%v", status, st, body)
	}
	rs, entries, _ := m6Roster(h, token)
	if rs != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, 期望 200", rs)
	}
	for _, e := range entries {
		if e.StudentNo == "01" {
			t.Errorf("roster 仍含旧学号 01: %+v", entries)
		}
	}

	// 不存在 → 404
	status, _, body = m6PatchStudent(h, token, 999999, map[string]string{"name": "X"})
	expectError(t, status, body, http.StatusNotFound, "改不存在的学生")

	// 已删除（垃圾桶内不在册）→ 404
	c := m6MustCreateStudent(t, h, token, "赵已删", "03")
	if status, _ := m6DeleteStudent(h, token, c.ID); status != http.StatusOK {
		t.Fatalf("删除赵已删状态码 = %d, 期望 200", status)
	}
	status, _, body = m6PatchStudent(h, token, c.ID, map[string]string{"name": "Y"})
	expectError(t, status, body, http.StatusNotFound, "改已删除的学生")
}

// ---------- T6 名单删与垃圾桶 ----------

// T6 删与垃圾桶：删后 roster 不含、trash 含（deletedAt 非空）；再删 404；
// 宠物与流水仍在库（直查 DB）；teacher/points 打该学号 404（入口封死）。
// 命令: go test ./server/ -run TestM6_T6_DeleteAndTrash -v
func TestM6_T6_DeleteAndTrash(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t6@example.com", "六年一班")
	a := m6MustCreateStudent(t, h, token, "钱进桶", "01")
	m6MustCreateStudent(t, h, token, "孙在册", "02")

	// 给 01 发宠物 + 加分（产生宠物流水，验证删除后保留）
	if status, _, body := m6AdoptFor(h, token, "01"); status != http.StatusOK {
		t.Fatalf("代领状态码 = %d, 期望 200; body=%v", status, body)
	}
	if status, pet, added, body := m6AddPointsFor(h, token, m6PointsReq{
		StudentNo: "01", Reason: "课堂表现", Value: 5, RequestID: "t6-1",
	}); status != http.StatusOK || !added || pet.Points != 5 {
		t.Fatalf("加分 = (status=%d added=%v points=%d), 期望 (200 true 5); body=%v", status, added, pet.Points, body)
	}

	// 记录宠物 id（删除后按 id 直查仍在库）
	db := m6OpenDB(t, dbPath)
	var petID int64
	if err := db.QueryRow(`SELECT p.id FROM pets p JOIN students s ON p.student_id = s.id WHERE s.student_no = '01'`).Scan(&petID); err != nil {
		t.Fatalf("查询 01 的宠物失败: %v", err)
	}

	// 删除 → 200（软删）
	if status, body := m6DeleteStudent(h, token, a.ID); status != http.StatusOK {
		t.Fatalf("删除状态码 = %d, 期望 200; body=%v", status, body)
	}

	// roster 不含 01、仍含 02
	rs, entries, _ := m6Roster(h, token)
	if rs != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, 期望 200", rs)
	}
	if len(entries) != 1 || entries[0].StudentNo != "02" {
		t.Errorf("删除后 roster = %+v, 期望仅剩 02", entries)
	}

	// trash 含该生，deletedAt 非空
	ts, items, _ := m6TrashList(h, token)
	if ts != http.StatusOK {
		t.Fatalf("trash 状态码 = %d, 期望 200", ts)
	}
	found := false
	for _, it := range items {
		if it.ID == a.ID {
			found = true
			if it.Name != "钱进桶" || it.StudentNo != "01" {
				t.Errorf("trash 条目 = %+v, 期望 name=钱进桶 studentNo=01", it)
			}
			if it.DeletedAt == "" {
				t.Errorf("trash 条目 deletedAt 为空串, 期望非空删除时间")
			}
		}
	}
	if !found {
		t.Errorf("trash = %+v, 缺少已删除的 01", items)
	}

	// 再删 → 404
	status, body := m6DeleteStudent(h, token, a.ID)
	expectError(t, status, body, http.StatusNotFound, "重复删除")

	// 宠物与流水仍在库（软删只藏名单，不动物理数据）
	if n := m6Count(t, db, `SELECT COUNT(*) FROM pets WHERE id = ?`, petID); n != 1 {
		t.Errorf("软删后宠物行数 = %d, 期望 1（保留）", n)
	}
	if n := m6Count(t, db, `SELECT COUNT(*) FROM point_logs WHERE pet_id = ?`, petID); n != 1 {
		t.Errorf("软删后流水行数 = %d, 期望 1（保留）", n)
	}

	// teacher/points 打该学号 → 404（已删学生不参与业务，入口封死）
	status, _, _, body = m6AddPointsFor(h, token, m6PointsReq{
		StudentNo: "01", Reason: "作业优秀", Value: 1, RequestID: "t6-2",
	})
	expectError(t, status, body, http.StatusNotFound, "给已删学生加分")
}

// ---------- T7 恢复 ----------

// T7 恢复：无冲突恢复 200 回 roster；学号被在册学生占用恢复 409；不存在 404。
// 冲突构造：删 A(01) → 新增 B(01)（软删学号可复用）→ 恢复 A → 409。
// 命令: go test ./server/ -run TestM6_T7_Restore -v
func TestM6_T7_Restore(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t7@example.com", "七年一班")
	a := m6MustCreateStudent(t, h, token, "孙甲", "01")
	m6MustCreateStudent(t, h, token, "孙乙", "02")

	// 无冲突恢复：删 A → 恢复 → 200 回 roster
	if status, _ := m6DeleteStudent(h, token, a.ID); status != http.StatusOK {
		t.Fatalf("删除 A 状态码 = %d, 期望 200", status)
	}
	status, st, body := m6RestoreStudent(h, token, a.ID)
	if status != http.StatusOK {
		t.Fatalf("无冲突恢复状态码 = %d, 期望 200; body=%v", status, body)
	}
	if st.ID != a.ID || st.StudentNo != "01" {
		t.Errorf("恢复响应 = %+v, 期望 id=%d studentNo=01", st, a.ID)
	}
	if rs, entries, _ := m6Roster(h, token); rs != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, 期望 200", rs)
	} else {
		got := map[string]bool{}
		for _, e := range entries {
			got[e.StudentNo] = true
		}
		if !got["01"] || !got["02"] {
			t.Errorf("恢复后 roster 学号集合 = %v, 期望含 01 与 02", got)
		}
	}

	// 冲突恢复：再删 A → 新增同学号 B（须成功）→ 恢复 A → 409
	if status, _ := m6DeleteStudent(h, token, a.ID); status != http.StatusOK {
		t.Fatalf("再次删除 A 状态码 = %d, 期望 200", status)
	}
	m6MustCreateStudent(t, h, token, "李占用", "01")
	status, _, body = m6RestoreStudent(h, token, a.ID)
	expectError(t, status, body, http.StatusConflict, "学号被在册学生占用的恢复")

	// 不存在 → 404
	status, _, body = m6RestoreStudent(h, token, 999999)
	expectError(t, status, body, http.StatusNotFound, "恢复不存在的学生")
}

// ---------- T8 无 token / 篡改 token → 401 ----------

// T8 鉴权矩阵：无 token 与篡改 token 打全部教师端点 → 401；有效 token 对照 200。
// （学生式 token 在 M6 下无法合法获得——学生自助途径全部下线，伪造需 HMAC 密钥，
// 角色隔离语义由本 401 矩阵 + T10 路由下线共同覆盖，见 T11 说明。）
// 命令: go test ./server/ -run TestM6_T8_AuthRequired -v
func TestM6_T8_AuthRequired(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t8@example.com", "八年一班")
	s := m6MustCreateStudent(t, h, token, "周目标", "01")

	endpoints := []struct {
		method string
		target string
		body   any
	}{
		{http.MethodPost, "/api/teacher/students", map[string]string{"name": "x", "studentNo": "02"}},
		{http.MethodPatch, m6StudentPath(s.ID, ""), map[string]string{"name": "x"}},
		{http.MethodDelete, m6StudentPath(s.ID, ""), nil},
		{http.MethodGet, "/api/teacher/trash", nil},
		{http.MethodPost, m6StudentPath(s.ID, "restore"), nil},
		{http.MethodPost, fmt.Sprintf("/api/teacher/pets/%d/name", s.ID), map[string]string{"name": "x"}},
		{http.MethodPost, "/api/teacher/adopt", map[string]string{"studentNo": "01"}},
		{http.MethodPost, "/api/teacher/points", m6PointsReq{StudentNo: "01", Reason: "x", Value: 1}},
		{http.MethodGet, "/api/teacher/roster", nil},
	}

	// 有效 token 对照：任一端点可进鉴权（选 roster 验 200）
	if status, _, _ := m6Roster(h, token); status != http.StatusOK {
		t.Fatalf("有效 token 调 roster 状态码 = %d, 期望 200（对照失败，后续 401 无意义）", status)
	}

	for _, tok := range []string{"", m6BadToken(token)} {
		for _, ep := range endpoints {
			resp, body := doJSON(t, h, ep.method, ep.target, tok, ep.body)
			expectError(t, resp.StatusCode, body, http.StatusUnauthorized,
				fmt.Sprintf("%s %s（token=%q）", ep.method, ep.target, tok))
		}
	}
}

// ---------- T9 教师改宠物名 ----------

// T9 教师改宠物名：adopt 后改名 200；再改又 200（无一次限制）；
// 不存在学生 404；无宠物学生 404；name 空白 400、>24 rune 400。
// 命令: go test ./server/ -run TestM6_T9_TeacherRenamePet -v
func TestM6_T9_TeacherRenamePet(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t9@example.com", "九年一班")
	a := m6MustCreateStudent(t, h, token, "周有宠", "01")
	if status, _, body := m6AdoptFor(h, token, "01"); status != http.StatusOK {
		t.Fatalf("代领状态码 = %d, 期望 200; body=%v", status, body)
	}

	// 改名 → 200 {pet}，name 生效
	status, pet, body := m6RenamePet(h, token, a.ID, "小云朵")
	if status != http.StatusOK {
		t.Fatalf("改名状态码 = %d, 期望 200; body=%v", status, body)
	}
	if pet.Name != "小云朵" {
		t.Errorf("改名响应 pet.name = %q, 期望 小云朵", pet.Name)
	}

	// 再改 → 200（M6 移除 M1 的一次性限制）
	status, pet, body = m6RenamePet(h, token, a.ID, "小彩虹")
	if status != http.StatusOK {
		t.Fatalf("再次改名状态码 = %d, 期望 200（无一次限制）; body=%v", status, body)
	}
	if pet.Name != "小彩虹" {
		t.Errorf("再次改名响应 pet.name = %q, 期望 小彩虹", pet.Name)
	}

	// 不存在学生 → 404
	status, _, body = m6RenamePet(h, token, 999999, "X")
	expectError(t, status, body, http.StatusNotFound, "给不存在学生改名")

	// 无宠物学生 → 404
	b := m6MustCreateStudent(t, h, token, "吴无宠", "02")
	status, _, body = m6RenamePet(h, token, b.ID, "Y")
	expectError(t, status, body, http.StatusNotFound, "给无宠物学生改名")

	// name 空白 → 400；>24 rune → 400
	status, _, body = m6RenamePet(h, token, a.ID, "   ")
	expectError(t, status, body, http.StatusBadRequest, "空白宠物名")
	status, _, body = m6RenamePet(h, token, a.ID, strings.Repeat("超", 25))
	expectError(t, status, body, http.StatusBadRequest, "25 rune 宠物名")
}

// ---------- T10 学生端点下线 404 ----------

// T10 下线 404：9 个学生端点 + /api/teacher/passcode，GET 与 POST 逐个断言 404。
// （任务书写「11 个端点」：按路径计为 10 条；第 11 条是旧口令式 /api/teacher/login，
// 该路径被邮箱式登录占用不能断言 404，其行为切换由 T14 断言。）
// 已知坑：GET 未显式处理会落入 "GET /" SPA 回退返回 200、POST 可能 405，均不符合契约。
// 命令: go test ./server/ -run TestM6_T10_StudentEndpointsGone -v
func TestM6_T10_StudentEndpointsGone(t *testing.T) {
	h, _ := m6Handler(t)
	gone := []string{
		"/api/join",
		"/api/eggs",
		"/api/adopt",
		"/api/pet/me",
		"/api/pet/name",
		"/api/points",
		"/api/pet/me/log",
		"/api/dex",
		"/api/class/wall",
		"/api/teacher/passcode", // M4 口令修改端点随口令式登录一并下线
	}
	for _, path := range gone {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			var body any
			if method == http.MethodPost {
				body = map[string]string{"x": "y"}
			}
			if code := m6Status(h, method, path, "", body); code != http.StatusNotFound {
				t.Errorf("%s %s 状态码 = %d, 期望 404（下线）", method, path, code)
			}
		}
	}
}

// ---------- T11 学生视角终局确认（Skip 存档） ----------

// T11 说明用例（恒 Skip）：M6 下线全部学生自助途径后，学生式 token 无法合法获得
// （签发需 HMAC 密钥，伪造不可行）；「学生 token 打教师端点 → 403」的 errWrongRole
// 路径不再可达，鉴权语义由 T8 的 401 矩阵 + T10 的路由下线共同覆盖。
// 命令: go test ./server/ -run TestM6_T11_StudentRole -v
func TestM6_T11_StudentRole(t *testing.T) {
	t.Skip("学生自助途径已全部下线，学生式 token 无法合法签发（伪造需 HMAC 密钥）；角色语义由 T8/T10 覆盖")
}

// ---------- T12 roster 只含在册 ----------

// T12 roster 过滤：建 3 人删 1 人 → roster 恰 2 条（不含已删），trash 恰 1 条。
// 命令: go test ./server/ -run TestM6_T12_RosterFilter -v
func TestM6_T12_RosterFilter(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t12@example.com", "十二班")
	m6MustCreateStudent(t, h, token, "甲", "01")
	b := m6MustCreateStudent(t, h, token, "乙", "02")
	m6MustCreateStudent(t, h, token, "丙", "03")
	if status, _ := m6DeleteStudent(h, token, b.ID); status != http.StatusOK {
		t.Fatalf("删除乙状态码 = %d, 期望 200", status)
	}

	rs, entries, _ := m6Roster(h, token)
	if rs != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, 期望 200", rs)
	}
	if len(entries) != 2 {
		t.Fatalf("roster 条数 = %d, 期望 2: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.StudentNo == "02" {
			t.Errorf("roster 仍含已删学号 02: %+v", entries)
		}
	}
	if ts, items, _ := m6TrashList(h, token); ts != http.StatusOK || len(items) != 1 {
		t.Errorf("trash = (status=%d items=%d), 期望 (200 1)", ts, len(items))
	}
}

// ---------- T13 M5 老库重建迁移 ----------

// T13 迁移：直建 M5 老 schema（students 无 deleted_at 带表内 UNIQUE、含真实数据）
// → server.New(dbPath) 成功；数据行数无损；students 有 deleted_at；
// 部分唯一索引 idx_students_class_no_live 存在；旧表内 UNIQUE 约束消失
// （同班同学号且均软删的两行可插入），且在册唯一性仍被索引拦截。
// 命令: go test ./server/ -run TestM6_T13_MigrateM5OldDB -v
func TestM6_T13_MigrateM5OldDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m6-old-m5.db")
	m6SeedM5OldDB(t, dbPath)

	h := m6NewAt(t, dbPath) // 迁移失败（缺 deleted_at 的表无法承载新逻辑）会 Fatal
	_ = h

	db := m6OpenDB(t, dbPath)

	// 数据无损：2 学生 / 1 宠物 / 1 流水，宠物 19 分与姓名原样
	if n := m6Count(t, db, `SELECT COUNT(*) FROM students`); n != 2 {
		t.Errorf("迁移后 students 行数 = %d, 期望 2（无损）", n)
	}
	if n := m6Count(t, db, `SELECT COUNT(*) FROM pets`); n != 1 {
		t.Errorf("迁移后 pets 行数 = %d, 期望 1（无损）", n)
	}
	if n := m6Count(t, db, `SELECT COUNT(*) FROM point_logs`); n != 1 {
		t.Errorf("迁移后 point_logs 行数 = %d, 期望 1（无损）", n)
	}
	var (
		name          string
		points        int
		deletedIsNull bool
	)
	if err := db.QueryRow(
		`SELECT s.name, p.points, s.deleted_at IS NULL FROM students s
		 JOIN pets p ON p.student_id = s.id WHERE s.student_no = 'S001'`,
	).Scan(&name, &points, &deletedIsNull); err != nil {
		t.Fatalf("查询迁移后的老学生数据失败: %v", err)
	}
	if name != "老同学甲" || points != 19 || !deletedIsNull {
		t.Errorf("老学生数据 = (%q,%d,deletedAtNull=%v), 期望 (老同学甲,19,true)", name, points, deletedIsNull)
	}

	// schema：deleted_at 列 + 部分唯一索引存在
	if !m6HasColumn(t, db, "students", "deleted_at") {
		t.Errorf("迁移后 students 缺少 deleted_at 列")
	}
	if !m6HasIndex(t, db, "idx_students_class_no_live") {
		t.Errorf("sqlite_master 缺少部分唯一索引 idx_students_class_no_live")
	}

	// 旧表内 UNIQUE 消失：同班同学号且均软删的两行均可插入
	cid := m6ClassIDByCode(t, dbPath, "m6old")
	for _, nm := range []string{"已删甲", "已删乙"} {
		if _, err := db.Exec(
			`INSERT INTO students(class_id, name, student_no, created_at, deleted_at)
			 VALUES(?, ?, 'S009', datetime('now'), '2026-01-01 00:00:00')`, cid, nm,
		); err != nil {
			t.Errorf("插入软删同号学生 %s 失败（旧 UNIQUE 约束应已移除）: %v", nm, err)
		}
	}
	// 在册唯一性仍生效：两行同号且均未删，第二行必须被部分唯一索引拦截
	if _, err := db.Exec(
		`INSERT INTO students(class_id, name, student_no, created_at) VALUES(?, '在册甲', 'S010', datetime('now'))`, cid,
	); err != nil {
		t.Fatalf("插入首个在册 S010 失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO students(class_id, name, student_no, created_at) VALUES(?, '在册乙', 'S010', datetime('now'))`, cid,
	); err == nil {
		t.Errorf("两行在册同号插入竟成功：部分唯一索引 WHERE deleted_at IS NULL 未生效")
	}
	// 混合：一在册一软删同号 → 允许（软删行不占在册唯一性）
	if _, err := db.Exec(
		`INSERT INTO students(class_id, name, student_no, created_at, deleted_at)
		 VALUES(?, '软删丙', 'S010', datetime('now'), '2026-01-02 00:00:00')`, cid,
	); err != nil {
		t.Errorf("在册+软删同号插入失败（软删不应占用在册唯一性）: %v", err)
	}
}

// ---------- T14 迁移幂等 + 口令登录下线 + token 跨重启 ----------

// T14 迁移后回归（任务书口径：老库存量班级无教师账号，聚焦数据无损 + New() 幂等 +
// 口令式登录下线；教师操作回归由 T3 全新链路覆盖）：
// 老库迁移成功 → 再次 New() 幂等无损 → 教师 token 跨重启有效（密钥持久化）→
// 旧口令式 login 请求体（classCode/teacherPasscode）绝不再 200（钉死 400/401）。
// 命令: go test ./server/ -run TestM6_T14_MigrateIdempotentAndLoginSwitch -v
func TestM6_T14_MigrateIdempotentAndLoginSwitch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m6-old-restart.db")
	m6SeedM5OldDB(t, dbPath)

	h1 := m6NewAt(t, dbPath)
	token := m6RegisterTeacher(t, h1, dbPath, "t14@example.com", "十四班")
	if c, ok := h1.(interface{ Close() error }); ok {
		_ = c.Close() // 模拟重启（cleanup 的二次 Close 幂等）
	}

	h2 := m6NewAt(t, dbPath) // 重启再 migrate：不得重复重建/报错
	db := m6OpenDB(t, dbPath)
	if n := m6Count(t, db, `SELECT COUNT(*) FROM students WHERE student_no IN ('S001','S002')`); n != 2 {
		t.Errorf("重启迁移后老学生行数 = %d, 期望 2（幂等无损）", n)
	}
	if !m6HasIndex(t, db, "idx_students_class_no_live") {
		t.Errorf("重启迁移后部分唯一索引丢失（迁移须幂等）")
	}

	// 教师 token 跨重启仍有效（签名密钥持久化在 meta）
	if rs, rbody, _ := m6Roster(h2, token); rs != http.StatusOK {
		t.Errorf("重启后旧教师 token 调 roster 状态码 = %d, 期望 200; body=%v", rs, rbody)
	}

	// M4 口令式登录已死：旧请求体打 /api/teacher/login 不得 200
	// （邮箱式登录下该 body 因缺 email/password 被拒；400/401 皆可，200 不可）
	status, body, _ := m6Do(h2, http.MethodPost, "/api/teacher/login", "", map[string]string{
		"classCode":       "m6old",
		"teacherPasscode": "legacy-passcode",
	})
	if status == http.StatusOK {
		t.Errorf("旧口令式登录返回 200（口令式必须下线）: %v", body)
	} else if status != http.StatusBadRequest && status != http.StatusUnauthorized {
		t.Errorf("旧口令式登录状态码 = %d, 期望 400 或 401; body=%v", status, body)
	}
}

// ---------- T15 迁移后老学生的垃圾桶恢复唯一性 ----------

// T15 迁移语义核心：老库迁移而来的学生 → 删除（软）→ 新增同学号在册 → 恢复 → 409。
// 存量班级无教师账号，教师身份用 meta.token_secret 按契约格式直签（见 helper 注释）。
// 命令: go test ./server/ -run TestM6_T15_TrashUniquenessOnMigratedDB -v
func TestM6_T15_TrashUniquenessOnMigratedDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m6-old-trash.db")
	m6SeedM5OldDB(t, dbPath)
	h := m6NewAt(t, dbPath)

	cid := m6ClassIDByCode(t, dbPath, "m6old")
	token := m6TeacherTokenForClass(t, dbPath, cid)

	// 迁移后的老学生在 roster 可见
	rs, entries, _ := m6Roster(h, token)
	if rs != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, 期望 200", rs)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.StudentNo] = true
	}
	if !got["S001"] || !got["S002"] {
		t.Fatalf("迁移后 roster 学号集合 = %v, 期望含 S001/S002", got)
	}

	db := m6OpenDB(t, dbPath)
	var sid int64
	if err := db.QueryRow(`SELECT id FROM students WHERE class_id = ? AND student_no = 'S001'`, cid).Scan(&sid); err != nil {
		t.Fatalf("查询老学生 S001 失败: %v", err)
	}

	// 删除（软）→ 新增同学号在册（部分唯一索引放行软删学号复用）→ 恢复 → 409
	if status, _ := m6DeleteStudent(h, token, sid); status != http.StatusOK {
		t.Fatalf("删除老学生状态码 = %d, 期望 200", status)
	}
	m6MustCreateStudent(t, h, token, "转学来的新同学", "S001")
	status, _, body := m6RestoreStudent(h, token, sid)
	expectError(t, status, body, http.StatusConflict, "迁移库上学号被占的恢复")
}

// ---------- T16 清理任务 ----------

// T16 清理任务：构造 deleted_at=100 天前与 89 天前两学生（各带宠物+流水）
// → 再次 server.New(dbPath)（清理在启动时执行一次）→ 100 天者硬删
// （学生+宠物+流水全没）、89 天者三者全保留。
// deleted_at 先由 API 软删写入（格式为实现自定义），再直写 DB 平移到目标时间，
// 两种主流存储格式（unix 秒 / datetime 字符串）均兼容。
// 命令: go test ./server/ -run TestM6_T16_CleanupExpiredStudents -v
func TestM6_T16_CleanupExpiredStudents(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m6-cleanup.db")
	h1 := m6NewAt(t, dbPath)
	token := m6RegisterTeacher(t, h1, dbPath, "t16@example.com", "十六班")
	old100 := m6MustCreateStudent(t, h1, token, "清理甲", "01")
	old89 := m6MustCreateStudent(t, h1, token, "清理乙", "02")
	for i, no := range []string{"01", "02"} {
		if status, _, body := m6AdoptFor(h1, token, no); status != http.StatusOK {
			t.Fatalf("给学生 %s 代领状态码 = %d, 期望 200; body=%v", no, status, body)
		}
		if status, _, added, body := m6AddPointsFor(h1, token, m6PointsReq{
			StudentNo: no, Reason: "课堂表现", Value: 1, RequestID: fmt.Sprintf("t16-%d", i),
		}); status != http.StatusOK || !added {
			t.Fatalf("给学生 %s 加分 = (status=%d added=%v), 期望 (200 true); body=%v", no, status, added, body)
		}
	}
	// 软删两生（deleted_at 由实现写入其自定义格式）
	for _, id := range []int64{old100.ID, old89.ID} {
		if status, _ := m6DeleteStudent(h1, token, id); status != http.StatusOK {
			t.Fatalf("删除学生 %d 状态码 = %d, 期望 200", id, status)
		}
	}

	// 记录宠物与流水锚点，并把 deleted_at 平移到 100 天前 / 89 天前
	db := m6OpenDB(t, dbPath)
	petID := func(studentID int64) int64 {
		var pid int64
		if err := db.QueryRow(`SELECT id FROM pets WHERE student_id = ?`, studentID).Scan(&pid); err != nil {
			t.Fatalf("查询学生 %d 的宠物失败: %v", studentID, err)
		}
		return pid
	}
	pet100, pet89 := petID(old100.ID), petID(old89.ID)
	m6ShiftDeletedAt(t, db, old100.ID, -(100 * 24 * time.Hour))
	m6ShiftDeletedAt(t, db, old89.ID, -(89 * 24 * time.Hour))

	// 重启：New() 启动时执行一次清理
	if c, ok := h1.(interface{ Close() error }); ok {
		_ = c.Close()
	}
	m6NewAt(t, dbPath)

	// 100 天者：学生+宠物+流水硬删
	if n := m6Count(t, db, `SELECT COUNT(*) FROM students WHERE id = ?`, old100.ID); n != 0 {
		t.Errorf("100 天前删除的学生仍存在（行数 %d）, 期望硬删", n)
	}
	if n := m6Count(t, db, `SELECT COUNT(*) FROM pets WHERE id = ?`, pet100); n != 0 {
		t.Errorf("100 天前删除学生的宠物仍存在（行数 %d）, 期望连带硬删", n)
	}
	if n := m6Count(t, db, `SELECT COUNT(*) FROM point_logs WHERE pet_id = ?`, pet100); n != 0 {
		t.Errorf("100 天前删除学生的流水仍存在（行数 %d）, 期望连带硬删", n)
	}
	// 89 天者：三者全保留（3 个月内不清理）
	if n := m6Count(t, db, `SELECT COUNT(*) FROM students WHERE id = ?`, old89.ID); n != 1 {
		t.Errorf("89 天前删除的学生行数 = %d, 期望 1（3 个月内保留）", n)
	}
	if n := m6Count(t, db, `SELECT COUNT(*) FROM pets WHERE id = ?`, pet89); n != 1 {
		t.Errorf("89 天前删除学生的宠物行数 = %d, 期望 1（保留）", n)
	}
	if n := m6Count(t, db, `SELECT COUNT(*) FROM point_logs WHERE pet_id = ?`, pet89); n != 1 {
		t.Errorf("89 天前删除学生的流水行数 = %d, 期望 1（保留）", n)
	}
}

// ---------- T17 教师前端契约 ----------

// T17 前端：GET / 返回教师 SPA（200 + text/html），含登录/注册/工作台/垃圾桶
// 四视图标记（id="xxx-view" 或 data-view="xxx" 等价形式任一）+ viewport device-width
// （M5 契约不回退）；GET /style.css 仍 200。
// 命令: go test ./server/ -run TestM6_T17_TeacherSPA -v
func TestM6_T17_TeacherSPA(t *testing.T) {
	h := newHandler(t)

	resp := performJSON(h, http.MethodGet, "/", "", nil)
	rawBody := readAll(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / 状态码 = %d, 期望 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, 期望包含 text/html", ct)
	}
	html := string(rawBody)
	for view, markers := range map[string][]string{
		"登录":  {`id="login-view"`, `data-view="login"`},
		"注册":  {`id="register-view"`, `data-view="register"`},
		"工作台": {`id="work-view"`, `data-view="work"`},
		"垃圾桶": {`id="trash-view"`, `data-view="trash"`},
	} {
		hit := false
		for _, m := range markers {
			if strings.Contains(html, m) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("GET / 缺少%s视图标记（任一即可）: %v", view, markers)
		}
	}
	if !strings.Contains(html, `width=device-width`) {
		t.Errorf("GET / 缺少 viewport width=device-width（M5 契约不得回退）")
	}

	if code := m6Status(h, http.MethodGet, "/style.css", "", nil); code != http.StatusOK {
		t.Errorf("GET /style.css 状态码 = %d, 期望 200（M5 契约不得回退）", code)
	}
}

// ---------- T18 方法级 405 ----------

// T18 405（沿用 M2/M4 显式注册模式）：新名单端点的非允许方法一律 405 + Allow 头；
// 沿用端点（adopt/points/roster）与注册/登录/发码口的 405 回归。
// 命令: go test ./server/ -run TestM6_T18_MethodNotAllowed -v
func TestM6_T18_MethodNotAllowed(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t18@example.com", "十八班")
	s := m6MustCreateStudent(t, h, token, "方法生", "01")

	cases := []struct{ method, target string }{
		// 新名单端点
		{http.MethodGet, "/api/teacher/students"},
		{http.MethodPut, "/api/teacher/students"},
		{http.MethodPatch, "/api/teacher/students"},
		{http.MethodGet, m6StudentPath(s.ID, "restore")},
		{http.MethodPut, m6StudentPath(s.ID, "restore")},
		{http.MethodPatch, m6StudentPath(s.ID, "restore")},
		{http.MethodDelete, m6StudentPath(s.ID, "restore")},
		{http.MethodPost, "/api/teacher/trash"},
		{http.MethodPut, "/api/teacher/trash"},
		{http.MethodPatch, "/api/teacher/trash"},
		{http.MethodDelete, "/api/teacher/trash"},
		{http.MethodGet, fmt.Sprintf("/api/teacher/pets/%d/name", s.ID)},
		{http.MethodPut, fmt.Sprintf("/api/teacher/pets/%d/name", s.ID)},
		{http.MethodPatch, fmt.Sprintf("/api/teacher/pets/%d/name", s.ID)},
		{http.MethodDelete, fmt.Sprintf("/api/teacher/pets/%d/name", s.ID)},
		// 免鉴权口（POST-only）
		{http.MethodGet, "/api/teacher/email-code"},
		{http.MethodPut, "/api/teacher/email-code"},
		{http.MethodDelete, "/api/teacher/email-code"},
		{http.MethodGet, "/api/teacher/register"},
		{http.MethodPut, "/api/teacher/register"},
		{http.MethodDelete, "/api/teacher/register"},
		{http.MethodGet, "/api/teacher/login"},
		{http.MethodPut, "/api/teacher/login"},
		{http.MethodDelete, "/api/teacher/login"},
		// 沿用端点回归（M4 已有，M6 不得回退）
		{http.MethodGet, "/api/teacher/adopt"},
		{http.MethodGet, "/api/teacher/points"},
		{http.MethodPost, "/api/teacher/roster"},
		{http.MethodPut, "/api/teacher/roster"},
	}
	for _, tc := range cases {
		resp := performJSON(h, tc.method, tc.target, token, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s 状态码 = %d, 期望 405", tc.method, tc.target, resp.StatusCode)
			continue
		}
		if resp.Header.Get("Allow") == "" {
			t.Errorf("%s %s 405 响应缺少 Allow 头", tc.method, tc.target)
		}
	}
}

// ---------- T19 沿用端点回归（teacher/adopt、teacher/points、roster） ----------

// T19 M4 端点行为不变（仅数据入口改为名单）：代领 200（M1 宠物形状、level1/points0、
// nextLevelPoints=20）与已领 409；加分 1..50 合法域、0/51 → 400；requestId 幂等；
// 跨班学号 404；流水 operator=teacher（直查 DB）；roster 反映 adopted 与累计分。
// 命令: go test ./server/ -run TestM6_T19_CarryOverEndpoints -v
func TestM6_T19_CarryOverEndpoints(t *testing.T) {
	h, dbPath := m6Handler(t)
	tokA := m6RegisterTeacher(t, h, dbPath, "t19a@example.com", "十九甲班")
	tokB := m6RegisterTeacher(t, h, dbPath, "t19b@example.com", "十九乙班")
	m6MustCreateStudent(t, h, tokA, "陈一", "01")
	m6MustCreateStudent(t, h, tokB, "他班生", "B01")

	// 代领：200 + M1 宠物形状
	status, pet, body := m6AdoptFor(h, tokA, "01")
	if status != http.StatusOK {
		t.Fatalf("代领状态码 = %d, 期望 200; body=%v", status, body)
	}
	if pet.Name == "" || pet.Species.ID == "" || pet.Species.Name == "" {
		t.Errorf("代领所得 pet 形状不完整: %+v", pet)
	}
	if pet.Level != 1 || pet.Points != 0 {
		t.Errorf("代领所得 pet = (level %d, points %d), 期望 (1, 0)", pet.Level, pet.Points)
	}
	if pet.NextLevelPoints == nil || *pet.NextLevelPoints != 20 {
		t.Errorf("代领所得 nextLevelPoints = %v, 期望指向 20（Lv2 阈值不变）", pet.NextLevelPoints)
	}
	// 已领养 → 409
	status, _, body = m6AdoptFor(h, tokA, "01")
	expectError(t, status, body, http.StatusConflict, "重复代领")

	// 分值合法域：0 / 51 → 400；1 / 50 合法
	for _, v := range []int{0, 51} {
		status, _, _, body := m6AddPointsFor(h, tokA, m6PointsReq{StudentNo: "01", Reason: "越界", Value: v})
		expectError(t, status, body, http.StatusBadRequest, fmt.Sprintf("value=%d 加分", v))
	}
	for _, v := range []int{1, 50} {
		status, _, added, body := m6AddPointsFor(h, tokA, m6PointsReq{
			StudentNo: "01", Reason: "合法", Value: v, RequestID: fmt.Sprintf("t19-v%d", v),
		})
		if status != http.StatusOK || !added {
			t.Errorf("value=%d 加分 = (status=%d added=%v), 期望 (200 true); body=%v", v, status, added, body)
		}
	}

	// requestId 幂等：同 requestId 重放 added=false 不重复计分
	status, pet, added, body := m6AddPointsFor(h, tokA, m6PointsReq{
		StudentNo: "01", Reason: "作业优秀", Value: 3, RequestID: "t19-dup",
	})
	if status != http.StatusOK || !added || pet.Points != 54 {
		t.Fatalf("教师加分 = (status=%d added=%v points=%d), 期望 (200 true 54); body=%v", status, added, pet.Points, body)
	}
	status, pet, added, body = m6AddPointsFor(h, tokA, m6PointsReq{
		StudentNo: "01", Reason: "参数不同也幂等", Value: 5, RequestID: "t19-dup",
	})
	if status != http.StatusOK || added || pet.Points != 54 {
		t.Errorf("同 requestId 异参重放 = (status=%d added=%v points=%d), 期望 (200 false 54); body=%v", status, added, pet.Points, body)
	}

	// 跨班学号 → 404（他班 B01 不是本班学生）
	status, _, _, body = m6AddPointsFor(h, tokA, m6PointsReq{StudentNo: "B01", Reason: "越权", Value: 1})
	expectError(t, status, body, http.StatusNotFound, "给别班学生加分")

	// 流水 operator=teacher 留痕（直查 DB；学生流水端点已下线，无 API 观察口）
	db := m6OpenDB(t, dbPath)
	var operator string
	if err := db.QueryRow(
		`SELECT pl.operator FROM point_logs pl
		 JOIN pets p ON pl.pet_id = p.id
		 JOIN students s ON p.student_id = s.id
		 WHERE s.student_no = '01'
		   AND s.class_id = (SELECT class_id FROM teachers WHERE email = 't19a@example.com')
		 ORDER BY pl.id DESC LIMIT 1`,
	).Scan(&operator); err != nil {
		t.Fatalf("查询最新流水 operator 失败: %v", err)
	}
	if operator != "teacher" {
		t.Errorf("最新流水 operator = %q, 期望 teacher", operator)
	}

	// roster 反映 adopted 与累计分（54）
	rs, entries, _ := m6Roster(h, tokA)
	if rs != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, 期望 200", rs)
	}
	if len(entries) != 1 || !entries[0].Adopted || entries[0].Points != 54 || entries[0].Name != "陈一" {
		t.Errorf("roster = %+v, 期望唯一学生陈一 adopted=true points=54", entries)
	}
}
