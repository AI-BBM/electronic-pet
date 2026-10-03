package server_test

// M6 对抗审查回灌用例（follow-up）：
// - T13b M1 老库直升（P1：request_id 缺列时启动崩）；
// - T20 roster 暴露 petName（前端改名可见性）；
// - T2b 邮箱大小写规范化（防变体多账号）；
// - T2c 密码按字节上限（防 72 字节超限 500）；
// - T2d 验证码尝试上限（防暴力猜码）；
// - T5b PATCH 原子性（撞号 409 不得已改姓名）。

import (
	"net/http"
	"strings"
	"testing"
)

// TestM6_T13b_MigrateM1OldDB M1 老库（point_logs 无 request_id/operator、
// classes 无 teacher_passcode、students 带 UNIQUE 无 deleted_at）直升 M6：
// server.New 必须成功，补列/索引/数据无损全过。
func TestM6_T13b_MigrateM1OldDB(t *testing.T) {
	dbPath, db := m6SeedM1OldDB(t)
	_ = db.Close()

	h := m6NewAt(t, dbPath) // 迁移 + 启动成功即第一断言

	mdb := m6OpenDB(t, dbPath)
	defer mdb.Close()
	if !m6HasColumn(t, mdb, "point_logs", "request_id") {
		t.Errorf("point_logs.request_id 未补列")
	}
	if !m6HasColumn(t, mdb, "point_logs", "operator") {
		t.Errorf("point_logs.operator 未补列")
	}
	if !m6HasIndex(t, mdb, "idx_point_logs_dedupe") {
		t.Errorf("idx_point_logs_dedupe 未建立")
	}
	if !m6HasIndex(t, mdb, "idx_students_class_no_live") {
		t.Errorf("idx_students_class_no_live 未建立")
	}
	if got := m6Count(t, mdb, `SELECT COUNT(*) FROM students`); got != 2 {
		t.Errorf("students 行数 = %d, 期望 2（数据无损）", got)
	}
	if got := m6Count(t, mdb, `SELECT COUNT(*) FROM point_logs`); got != 1 {
		t.Errorf("point_logs 行数 = %d, 期望 1（数据无损）", got)
	}

	// 教师端基本可用（迁移后 roster 可访问——存量班无教师账号，401 属预期；只验路由活着）
	status, _, _ := m6Do(h, http.MethodGet, "/api/teacher/roster", "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("迁移后 roster 无 token 状态码 = %d, 期望 401", status)
	}
}

// m6SeedM1OldDB 构造 M1 原始 schema 老库（含班级/学生/宠物/流水各最小数据）。
func m6SeedM1OldDB(t *testing.T) (string, interface{ Close() error }) {
	t.Helper()
	h, dbPath := m6Handler(t)
	_ = h // 不使用被测 handler，仅借其 TempDir 路径
	db := m6OpenDB(t, dbPath)
	const m1DDL = `
DROP TABLE IF EXISTS point_logs; DROP TABLE IF EXISTS pets; DROP TABLE IF EXISTS students;
DROP TABLE IF EXISTS classes; DROP TABLE IF EXISTS teachers; DROP TABLE IF EXISTS meta;
CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE classes (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	code       TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE students (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	class_id   INTEGER NOT NULL REFERENCES classes(id),
	name       TEXT NOT NULL,
	student_no TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(class_id, student_no)
);
CREATE TABLE pets (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	student_id INTEGER NOT NULL UNIQUE REFERENCES students(id),
	species_id TEXT NOT NULL,
	name       TEXT NOT NULL,
	name_customized INTEGER NOT NULL DEFAULT 0,
	level      INTEGER NOT NULL DEFAULT 1,
	points     INTEGER NOT NULL DEFAULT 0,
	egg_id     TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE point_logs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	pet_id     INTEGER NOT NULL REFERENCES pets(id),
	delta      INTEGER NOT NULL,
	reason     TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO classes(id, code) VALUES (1, 'C100001');
INSERT INTO students(id, class_id, name, student_no) VALUES (1, 1, '老学生甲', '01'), (2, 1, '老学生乙', '02');
INSERT INTO pets(id, student_id, species_id, name) VALUES (1, 1, 'cat', '老猫');
INSERT INTO point_logs(id, pet_id, delta, reason) VALUES (1, 1, 3, 'M1 时代加分');
`
	if _, err := db.Exec(m1DDL); err != nil {
		t.Fatalf("构造 M1 老库失败: %v", err)
	}
	return dbPath, db
}

// TestM6_T20_RosterIncludesPetName adopt + 改名后 roster 必须带 petName（前端改名可见）。
func TestM6_T20_RosterIncludesPetName(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "t20@test.com", "改名可见班")
	st := m6MustCreateStudent(t, h, token, "学生甲", "01")
	if status, _, body := m6AdoptFor(h, token, "01"); status != http.StatusOK {
		t.Fatalf("adopt 状态码 = %d, body=%v", status, body)
	}
	if status, _, body := m6RenamePet(h, token, st.ID, "小云朵"); status != http.StatusOK {
		t.Fatalf("rename 状态码 = %d, body=%v", status, body)
	}
	status, roster, body := m6Roster(h, token)
	if status != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, body=%v", status, body)
	}
	if len(roster) != 1 {
		t.Fatalf("roster 条数 = %d, 期望 1", len(roster))
	}
	if roster[0].PetName != "小云朵" {
		t.Errorf("roster[0].petName = %q, 期望 %q（教师改名须在花名册可见）", roster[0].PetName, "小云朵")
	}
}

// TestM6_T2b_EmailCaseNormalized 邮箱大小写规范化：大写变体发码与小写同键（限速
// 生效 429）；持有效码的大写变体注册落到同一条小写记录（不产生第二个账号）。
func TestM6_T2b_EmailCaseNormalized(t *testing.T) {
	h, dbPath := m6Handler(t)

	// 只发码不注册：小写 user@test.com 发码成功
	if status, _ := m6EmailCode(h, "user@test.com"); status != http.StatusOK {
		t.Fatalf("小写发码状态码 = %d, 期望 200", status)
	}
	// 大写变体发码：规范化后同键 → 60s 限速 429（规范化的直接证据）
	if status, body := m6EmailCode(h, "USER@test.com"); status != http.StatusTooManyRequests {
		t.Fatalf("大写变体发码状态码 = %d, 期望 429（同键限速）, body=%v", status, body)
	}

	// 持小写键的码以大写变体注册：应成功且落为同一条小写记录（非第二账号）
	code := m6ReadEmailCode(t, dbPath, "user@test.com")
	status, body := m6Register(h, "USER@test.com", code, "password8", "规范班")
	if status != http.StatusOK {
		t.Fatalf("大写变体注册状态码 = %d, 期望 200, body=%v", status, body)
	}

	mdb := m6OpenDB(t, dbPath)
	defer mdb.Close()
	if got := m6Count(t, mdb, `SELECT COUNT(*) FROM teachers WHERE email = 'user@test.com'`); got != 1 {
		t.Errorf("小写 teachers 记录数 = %d, 期望 1", got)
	}
	if got := m6Count(t, mdb, `SELECT COUNT(*) FROM teachers WHERE email = 'USER@test.com'`); got != 0 {
		t.Errorf("存在大写形态记录 = %d, 期望 0（必须规范化入库）", got)
	}
}

// TestM6_T2c_PasswordByteLimit 密码 30 个汉字（90 字节 > 72）应 400 而非 500。
func TestM6_T2c_PasswordByteLimit(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "bytes@test.com"
	if status, _ := m6EmailCode(h, email); status != http.StatusOK {
		t.Fatalf("发码状态码 = %d", status)
	}
	code := m6ReadEmailCode(t, dbPath, email)
	longPassword := strings.Repeat("密", 30) // 30 rune = 90 字节
	status, body := m6Register(h, email, code, longPassword, "字节班")
	if status != http.StatusBadRequest {
		t.Errorf("超字节密码注册状态码 = %d, 期望 400, body=%v", status, body)
	}
}

// TestM6_T2d_CodeAttemptLimit 连错 5 次后，正确码也应被作废拒绝（防暴力猜码）。
func TestM6_T2d_CodeAttemptLimit(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "brute@test.com"
	if status, _ := m6EmailCode(h, email); status != http.StatusOK {
		t.Fatalf("发码状态码 = %d", status)
	}
	correctCode := m6ReadEmailCode(t, dbPath, email)
	wrong := m6WrongCode(correctCode)

	var status int
	var body map[string]any
	for i := 0; i < 5; i++ {
		status, body = m6Register(h, email, wrong, "password8", "防爆破班")
		if status != http.StatusBadRequest {
			t.Fatalf("第 %d 次错码注册状态码 = %d, 期望 400, body=%v", i+1, status, body)
		}
	}
	// 第 6 次：即使拿正确码也必须被拒（已作废）
	status, body = m6Register(h, email, correctCode, "password8", "防爆破班")
	if status != http.StatusBadRequest {
		t.Errorf("达尝试上限后正确码注册状态码 = %d, 期望 400, body=%v", status, body)
	}
}

// TestM6_T5b_PatchAtomicity PATCH name+撞号 studentNo：409 后姓名不得已改。
func TestM6_T5b_PatchAtomicity(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m6RegisterTeacher(t, h, dbPath, "atomic@test.com", "原子性班")
	a := m6MustCreateStudent(t, h, token, "学生A", "01")
	_ = m6MustCreateStudent(t, h, token, "学生B", "02")

	status, _, body := m6PatchStudent(h, token, a.ID, map[string]string{
		"name":      "被误改的名字",
		"studentNo": "02", // 撞 B 的在册学号
	})
	if status != http.StatusConflict {
		t.Fatalf("PATCH 撞号状态码 = %d, 期望 409, body=%v", status, body)
	}

	rstatus, roster, rbody := m6Roster(h, token)
	if rstatus != http.StatusOK {
		t.Fatalf("roster 状态码 = %d, body=%v", rstatus, rbody)
	}
	for _, e := range roster {
		if e.StudentNo == "01" && e.Name != "学生A" {
			t.Errorf("撞号 409 后姓名被误改为 %q（PATCH 应原子）", e.Name)
		}
	}
}
