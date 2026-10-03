package server_test

// Issue #11 回归测试：M1 旧 DDL 建的库必须能被 migrate 平滑升级——
// 补 point_logs.request_id 列、建幂等索引，老数据无损，重启幂等；
// 升级后 /api/points 的幂等键约束真实生效。
// 命令: go test ./server/ -run TestMigrate_M1OldDatabase -v

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // 测试直连旧库 seed 数据

	"github.com/AI-BBM/electronic-pet/server"
)

// m1OldDDL 是 M1 版本（84eac60 之前）的建库语句：point_logs 无 request_id 列、无索引。
const m1OldDDL = `
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
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// seedOldM1DB 用 M1 旧 DDL 建库并预置数据：一名学生、一只 19 分宠物、一条老流水。
func seedOldM1DB(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(m1OldDDL); err != nil {
		t.Fatalf("执行 M1 旧 DDL 失败: %v", err)
	}
	seed := `
INSERT INTO classes (code) VALUES ('cold');
INSERT INTO students (class_id, name, student_no)
	SELECT id, '老同学', 'S001' FROM classes WHERE code = 'cold';
INSERT INTO pets (student_id, species_id, name, level, points, egg_id)
	SELECT id, 'cat', '老猫', 1, 19, 'egg-1' FROM students WHERE student_no = 'S001';
INSERT INTO point_logs (pet_id, delta, reason)
	SELECT id, 19, '入学礼包' FROM pets WHERE name = '老猫';
`
	if _, err := db.Exec(seed); err != nil {
		t.Fatalf("seed 旧库数据失败: %v", err)
	}
}

func TestMigrate_M1OldDatabase_UpgradeAndIdempotency(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old-m1.db")
	seedOldM1DB(t, dbPath)

	// 修复前此处 fatal：migrate: no such column: request_id（issue #11 现象）
	h, err := server.New(dbPath)
	if err != nil {
		t.Fatalf("server.New(M1 旧库) 返回错误: %v", err)
	}
	closeH := func(x http.Handler) {
		if c, ok := x.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}

	// 同班同学号 join 幂等找回老学生 → 老宠物（19 分）仍在
	token := mustJoin(t, h, "cold", "老同学", "S001")
	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pet/me 状态码 = %d, 期望 200; body=%v", resp.StatusCode, body)
	}
	if me := petFromAny(t, body["pet"]); me.Points != 19 || me.Level != 1 {
		t.Fatalf("老宠物数据 = (level %d, points %d), 期望 (1, 19)", me.Level, me.Points)
	}

	// 老数据上加分：19+1 → Lv2
	res := m2AddPoints(h, token, m2PointsRequest{Reason: "作业优秀", Value: 1, RequestID: "hotfix-1"})
	if res.Status != http.StatusOK || !res.Added || !res.LevelUp || res.Level != 2 || res.Pet.Points != 20 {
		t.Fatalf("旧库升级后加分 = (status=%d added=%v levelUp=%v level=%d points=%d), 期望 (200 true true 2 20)",
			res.Status, res.Added, res.LevelUp, res.Level, res.Pet.Points)
	}

	// 幂等键约束生效：同 requestId 重放不计分
	res = m2AddPoints(h, token, m2PointsRequest{Reason: "作业优秀", Value: 1, RequestID: "hotfix-1"})
	if res.Status != http.StatusOK || res.Added || res.Pet.Points != 20 {
		t.Fatalf("重放 = (status=%d added=%v points=%d), 期望 (200 false 20)", res.Status, res.Added, res.Pet.Points)
	}
	closeH(h)

	// 重启再 migrate：幂等（列已存在则跳过 ALTER，索引 IF NOT EXISTS），旧 token 仍有效
	h2, err := server.New(dbPath)
	if err != nil {
		t.Fatalf("重启 server.New(已升级库) 返回错误: %v", err)
	}
	defer closeH(h2)

	res = m2AddPoints(h2, token, m2PointsRequest{Reason: "作业优秀", Value: 1, RequestID: "hotfix-1"})
	if res.Status != http.StatusOK || res.Added || res.Pet.Points != 20 {
		t.Fatalf("重启后重放 = (status=%d added=%v points=%d), 期望 (200 false 20)", res.Status, res.Added, res.Pet.Points)
	}
	// 新 requestId 正常计分
	res = m2AddPoints(h2, token, m2PointsRequest{Reason: "课堂表现", Value: 3, RequestID: "hotfix-2"})
	if !res.Added || res.Pet.Points != 23 {
		t.Errorf("重启后新提交 = (added=%v points=%d), 期望 (true 23)", res.Added, res.Pet.Points)
	}
}
