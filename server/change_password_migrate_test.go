package server_test

// #53 P3 迁移回归（#11 教训流程：直建旧 schema → server.New() 升级）。
//
// M6 时代老库 = teachers 表无 pass_ver 列（其余与现行 store.go 一致）。老库 DDL
// 从当前工作区 store.go 的建表语句照抄、teachers 去掉 pass_ver 列——实现落地后
// 会把该列加进 CREATE TABLE，此处剥离即模拟真实存量老库。
//
// 契约（全部黑盒 HTTP + 直查 DB）：
//   - server.New 对老库成功（不得因缺列报错启动失败）；
//   - 迁移补列 pass_ver，存量教师默认 0；
//   - 老两段式 token（改密前签发）改密前访问教师端点 200（ver=0 向后兼容）；
//   - 改密后 pass_ver=1，老两段式 token 立即 401；
//   - 重启（再次 New）幂等：pass_ver 持久、新密码可登录。

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// m53SeedPass 是老库种子教师的密码（bcrypt 直写进老库，与注册链路无关）。
const m53SeedPass = "legacy-pass-123"

// m53M6EraOldDDL：M6 时代老库最小 schema——meta/classes/teachers/students。
// teachers 即现行 store.go DDL 去掉 pass_ver（实现会加列，抄时剥离）；
// students 已是 M6 口径（含 deleted_at）。pets/pet_skins/point_logs 缺席时
// 由 migrate 的 CREATE TABLE IF NOT EXISTS 自动补齐，不影响本回归目标。
const m53M6EraOldDDL = `
CREATE TABLE IF NOT EXISTS meta (
	k   TEXT PRIMARY KEY,
	v   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS classes (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	code             TEXT NOT NULL UNIQUE,
	teacher_passcode TEXT,
	created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS teachers (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	class_id   INTEGER NOT NULL UNIQUE REFERENCES classes(id),
	email      TEXT NOT NULL UNIQUE,
	pass_hash  TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS students (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	class_id   INTEGER NOT NULL REFERENCES classes(id),
	name       TEXT NOT NULL,
	student_no TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	deleted_at DATETIME
);
`

// m53SeedM6EraOldDB 直建 M6 老库并预置：一班（m53old）一教师（bcrypt 种子密码）
// 一学生。照 m6SeedM5OldDB 先例直连 sqlite 建库 seed。
func m53SeedM6EraOldDB(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开老库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(m53M6EraOldDDL); err != nil {
		t.Fatalf("执行 M6 老库 DDL 失败: %v", err)
	}
	// MinCost 加速测试；bcrypt 校验与 cost 无关
	hash, err := bcrypt.GenerateFromPassword([]byte(m53SeedPass), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("生成种子密码 hash 失败: %v", err)
	}
	seed := `
INSERT INTO classes (code) VALUES ('m53old');
INSERT INTO teachers (class_id, email, pass_hash)
	SELECT id, 'legacy@m53.example.com', ? FROM classes WHERE code = 'm53old';
INSERT INTO students (class_id, name, student_no)
	SELECT id, '迁移生', 'S001' FROM classes WHERE code = 'm53old';
`
	if _, err := db.Exec(seed, string(hash)); err != nil {
		t.Fatalf("seed 老库数据失败: %v", err)
	}
}

// T10 迁移回归：M6 老库（teachers 无 pass_ver）直升成功、补列默认 0、
// 老两段式 token 改密前有效 → 改密后失效；重启幂等。
// 命令: go test ./server/ -run TestM53MigrateM6EraOldDBAddsPassVer -v
func TestM53MigrateM6EraOldDBAddsPassVer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m53-m6-old.db")
	m53SeedM6EraOldDB(t, dbPath)
	email := "legacy@m53.example.com"

	h := m6NewAt(t, dbPath) // #11 教训：老库直升不得报错（迁移失败此处 Fatal）

	db := m6OpenDB(t, dbPath)
	if !m6HasColumn(t, db, "teachers", "pass_ver") {
		t.Fatalf("迁移后 teachers 缺少 pass_ver 列（PRAGMA→ALTER 迁移未生效）")
	}
	if ver := m53PassVer(t, dbPath, email); ver != 0 {
		t.Errorf("老库迁移后 pass_ver = %d, 期望 0（存量教师默认 ver 0）", ver)
	}

	// 改密前：老两段式 token 仍有效（ver=0 == pass_ver=0，向后兼容），且老库数据可见
	tokLegacy := m53LegacyToken(t, dbPath, email)
	if status, entries, body := m6Roster(h, tokLegacy); status != http.StatusOK || len(entries) != 1 {
		t.Errorf("改密前老两段式 token roster = (status=%d, entries=%d), 期望 (200, 1); body=%v",
			status, len(entries), body)
	}
	tokLogin := m53MustLogin(t, h, email, m53SeedPass, "老库迁移后")

	// 改密：pass_ver 0→1，老两段式 token 立即失效，种子密码立即作废
	if status, body := m53Change(h, tokLogin, m53SeedPass, m53NewPass); status != http.StatusOK {
		t.Fatalf("老库教师改密状态码 = %d, 期望 200; body=%v", status, body)
	}
	if ver := m53PassVer(t, dbPath, email); ver != 1 {
		t.Errorf("老库教师改密后 pass_ver = %d, 期望 1", ver)
	}
	m53ExpectStale(t, h, tokLegacy, "老两段式token(改密后)")
	if status, body := m6Login(h, email, m53SeedPass); status != http.StatusUnauthorized {
		t.Errorf("种子密码 login 状态码 = %d, 期望 401; body=%v", status, body)
	}

	// 重启幂等：再次 New() 不报错、pass_ver 持久为 1、新密码可登录且 token 可用
	if c, ok := h.(interface{ Close() error }); ok {
		_ = c.Close() // 模拟重启（t.Cleanup 的二次 Close 幂等）
	}
	h2 := m6NewAt(t, dbPath)
	if ver := m53PassVer(t, dbPath, email); ver != 1 {
		t.Errorf("重启迁移后 pass_ver = %d, 期望 1（幂等持久）", ver)
	}
	tokAfter := m53MustLogin(t, h2, email, m53NewPass, "重启后")
	if status, _, body := m6Roster(h2, tokAfter); status != http.StatusOK {
		t.Errorf("重启后新 token 调 roster 状态码 = %d, 期望 200; body=%v", status, body)
	}
}
