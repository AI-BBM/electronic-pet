package server_test

// M4 迁移回归（核心）：schema 变更（classes.teacher_passcode、point_logs.operator）
// 必须走「pragma 查列 → ALTER ADD → 老库 → migrate 成功」路径。
//   T9  M2 schema 老库升级：server.New 成功；直接读 DB 断言补列与默认值回填；
//       老学生 join 幂等找回；教师登录/加分正常；重启 migrate 幂等。
//   T10 新库默认路径：join 建班即生成非空随机教师密码；流水 operator 默认 'student'。
// 写法参照 server/migrate_hotfix_test.go（issue #11 教训流程）。

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // 测试直连旧库 seed 数据与断言迁移结果

	"github.com/AI-BBM/electronic-pet/server"
)

// t4M2OldDDL 是 M2 版本（M4 之前、即 main 现行 store.go）的建库语句：
// classes 无 teacher_passcode、point_logs 无 operator，其余（含 request_id 与索引）不变。
const t4M2OldDDL = `
CREATE TABLE IF NOT EXISTS meta (
	k   TEXT PRIMARY KEY,
	v   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS classes (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	code       TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS students (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	class_id   INTEGER NOT NULL REFERENCES classes(id),
	name       TEXT NOT NULL,
	student_no TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(class_id, student_no)
);
CREATE TABLE IF NOT EXISTS pets (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	student_id      INTEGER NOT NULL UNIQUE REFERENCES students(id),
	species_id      TEXT NOT NULL,
	name            TEXT NOT NULL,
	name_customized INTEGER NOT NULL DEFAULT 0,
	level           INTEGER NOT NULL DEFAULT 1,
	points          INTEGER NOT NULL DEFAULT 0,
	egg_id          TEXT,
	created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS point_logs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	pet_id     INTEGER NOT NULL REFERENCES pets(id),
	delta      INTEGER NOT NULL,
	reason     TEXT NOT NULL,
	request_id TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_point_logs_dedupe
	ON point_logs (pet_id, request_id) WHERE request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_point_logs_pet ON point_logs (pet_id, id);
`

// t4SeedM2OldDB 用 M2 旧 DDL 建库并预置数据：一班一学生一宠物（19 分）一条老流水。
func t4SeedM2OldDB(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(t4M2OldDDL); err != nil {
		t.Fatalf("执行 M2 旧 DDL 失败: %v", err)
	}
	seed := `
INSERT INTO classes (code) VALUES ('cm2');
INSERT INTO students (class_id, name, student_no)
	SELECT id, '老同学', 'S001' FROM classes WHERE code = 'cm2';
INSERT INTO pets (student_id, species_id, name, level, points, egg_id)
	SELECT id, 'cat', '老猫', 1, 19, 'egg-1' FROM students WHERE student_no = 'S001';
INSERT INTO point_logs (pet_id, delta, reason)
	SELECT id, 19, '入学礼包' FROM pets WHERE name = '老猫';
`
	if _, err := db.Exec(seed); err != nil {
		t.Fatalf("seed 旧库数据失败: %v", err)
	}
}

// T9 M2 老库升级（核心回归）。
// 命令: go test ./server/ -run TestT4Migrate_M2OldDatabase -v
// 预期: 实现落地前 FAIL（server.New 成功但 SELECT teacher_passcode 报 no such column）；
// 落地后 PASS：补列非空、operator 默认 'student' 回填、老数据无损、重启幂等。
func TestT4Migrate_M2OldDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old-m2.db")
	t4SeedM2OldDB(t, dbPath)

	// migrate 必须成功（不得因缺列崩溃）
	h, err := server.New(dbPath)
	if err != nil {
		t.Fatalf("server.New(M2 旧库) 返回错误: %v", err)
	}
	closeH := func(x http.Handler) {
		if c, ok := x.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}

	// 直接读 DB 断言：classes.teacher_passcode 已补且非空；point_logs.operator='student'
	db := t4OpenDB(t, dbPath)
	var pass string
	if err := db.QueryRow(`SELECT teacher_passcode FROM classes WHERE code = 'cm2'`).Scan(&pass); err != nil {
		t.Fatalf("读取迁移后的 teacher_passcode 失败（列未补?）: %v", err)
	}
	if pass == "" {
		t.Error("迁移补列后 teacher_passcode 为空串, 期望随机非空密码")
	}
	var operator string
	if err := db.QueryRow(`SELECT operator FROM point_logs WHERE reason = '入学礼包'`).Scan(&operator); err != nil {
		t.Fatalf("读取迁移后的 point_logs.operator 失败（列未补?）: %v", err)
	}
	if operator != "student" {
		t.Errorf("老流水 operator = %q, 期望 'student'（DEFAULT 回填存量行）", operator)
	}

	// 老学生同班同学号 join 幂等找回：老宠物（19 分）仍在
	studentTok := mustJoin(t, h, "cm2", "老同学", "S001")
	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", studentTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pet/me 状态码 = %d, 期望 200; body=%v", resp.StatusCode, body)
	}
	if me := petFromAny(t, body["pet"]); me.Points != 19 || me.Level != 1 {
		t.Fatalf("老宠物数据 = (level %d, points %d), 期望 (1, 19)（升级无损）", me.Level, me.Points)
	}

	// 教师用迁移生成的密码登录 → 加分正常（19+1 → Lv2），operator=teacher 留痕
	teacherTok := t4MustLogin(t, h, "cm2", pass)
	res := t4AddPoints(h, teacherTok, t4PointsRequest{StudentNo: "S001", Reason: "教师补分", Value: 1, RequestID: "t9-a"})
	if res.Status != http.StatusOK || !res.Added || !res.LevelUp || res.Level != 2 || res.Pet.Points != 20 {
		t.Fatalf("旧库升级后教师加分 = (status=%d added=%v levelUp=%v level=%d points=%d), 期望 (200 true true 2 20); body=%v",
			res.Status, res.Added, res.LevelUp, res.Level, res.Pet.Points, res.Body)
	}
	list := t4GetLog(t, h, studentTok, "")
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("升级后流水 total=%d items=%d, 期望 2/2", list.Total, len(list.Items))
	}
	if got := list.Items[0]; got.Operator != "teacher" || got.Value != 1 {
		t.Errorf("新流水首条 = %+v, 期望 operator=teacher value=1", got)
	}
	if got := list.Items[1]; got.Operator != "student" || got.Value != 19 {
		t.Errorf("老流水 = %+v, 期望 operator=student value=19", got)
	}

	closeH(h)

	// 重启再 migrate：幂等（列已存在则跳过 ALTER），密码不变、旧 token 仍有效、加分正常
	h2, err := server.New(dbPath)
	if err != nil {
		t.Fatalf("重启 server.New(已升级库) 返回错误: %v", err)
	}
	defer closeH(h2)
	if pass2 := t4GetPasscode(t, dbPath, "cm2"); pass2 != pass {
		t.Errorf("重启后 teacher_passcode 变化: %q → %q, 期望不变", pass, pass2)
	}
	res = t4AddPoints(h2, teacherTok, t4PointsRequest{StudentNo: "S001", Reason: "课堂表现", Value: 2, RequestID: "t9-b"})
	if res.Status != http.StatusOK || !res.Added || res.Pet.Points != 22 {
		t.Fatalf("重启后教师加分 = (status=%d added=%v points=%d), 期望 (200 true 22); body=%v",
			res.Status, res.Added, res.Pet.Points, res.Body)
	}
}

// T10 新库默认路径。
// 命令: go test ./server/ -run TestT4NewDB_Defaults -v
// 预期: 实现落地前 FAIL（teacher_passcode 列不存在）；落地后 PASS。
func TestT4NewDB_Defaults(t *testing.T) {
	h, dbPath := t4Handler(t)
	s1 := mustJoin(t, h, "ca", "甲", "S001")
	mustJoin(t, h, "cb", "乙", "S001")

	// join 建班即生成非空教师密码；不同班级各自随机
	db := t4OpenDB(t, dbPath)
	rows, err := db.Query(`SELECT code, teacher_passcode FROM classes ORDER BY code`)
	if err != nil {
		t.Fatalf("查询 classes.teacher_passcode 失败（新库建班应自带该列）: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var code, pass string
		if err := rows.Scan(&code, &pass); err != nil {
			t.Fatalf("scan classes 失败: %v", err)
		}
		got[code] = pass
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 classes 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("班级数 = %d, 期望 2: %v", len(got), got)
	}
	for code, pass := range got {
		if pass == "" {
			t.Errorf("班级 %s 的 teacher_passcode 为空串, 期望随机非空", code)
		}
	}
	if got["ca"] == got["cb"] {
		t.Errorf("两个班级的教师密码相同 (%q), 期望各自随机生成", got["ca"])
	}

	// 学生自助加分 → 流水 operator 默认 'student'
	mustAdopt(t, h, s1, "random")
	if res := m2AddPoints(h, s1, m2PointsRequest{Reason: "自我打卡", Value: 1, RequestID: "t10-1"}); res.Status != http.StatusOK {
		t.Fatalf("学生自加失败: status=%d body=%v", res.Status, res.Body)
	}
	var operator string
	if err := db.QueryRow(`SELECT operator FROM point_logs ORDER BY id DESC LIMIT 1`).Scan(&operator); err != nil {
		t.Fatalf("读取 point_logs.operator 失败（新库应自带该列）: %v", err)
	}
	if operator != "student" {
		t.Errorf("学生自加流水 operator = %q, 期望 'student'（默认值）", operator)
	}
}
