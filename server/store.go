package server

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite" // 注册 "sqlite" 驱动（纯 Go，无 CGO）
)

// openDB 打开 SQLite（WAL + busy_timeout，写并发由单连接串行化保证无 SQLITE_BUSY 泄漏）。
func openDB(dbPath string) (*sql.DB, error) {
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	const ddl = `
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
	pass_ver   INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
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
CREATE TABLE IF NOT EXISTS pet_skins (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	pet_id      INTEGER NOT NULL REFERENCES pets(id),
	scene_id    TEXT NOT NULL,
	acquired_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE (pet_id, scene_id)
);
CREATE TABLE IF NOT EXISTS point_logs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	pet_id     INTEGER NOT NULL REFERENCES pets(id),
	delta      INTEGER NOT NULL,
	reason     TEXT NOT NULL,
	request_id TEXT,
	operator   TEXT NOT NULL DEFAULT 'teacher',
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`
	// 注意：students 不在上方 DDL 中——它带 UNIQUE 约束的历史，M6 起唯一性
	// 改由部分唯一索引承担（垃圾桶软删除口径），必须走重建迁移（见 rebuildStudents）。
	if _, err := db.Exec(ddl); err != nil {
		return err
	}
	if err := rebuildStudents(db); err != nil {
		return err
	}
	if err := migrateM4Columns(db); err != nil {
		return err
	}
	if err := migrateTeacherPassVer(db); err != nil {
		return err
	}
	// M12（#51）：dedupe 索引升级为 (pet_id, type, request_id)——earn/spend
	// 两类流水的 request_id 命名空间相互独立（同 ID 加分与购买互不干扰）。
	// 旧索引不含 type，必须 DROP 重建（CREATE IF NOT EXISTS 不会改列）。
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_point_logs_dedupe`); err != nil {
		return err
	}
	const idx = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_point_logs_dedupe
	ON point_logs (pet_id, type, request_id) WHERE request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_point_logs_pet ON point_logs (pet_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_students_class_no_live
	ON students (class_id, student_no) WHERE deleted_at IS NULL;
`
	_, err := db.Exec(idx)
	return err
}

// migrateM4Columns 为 M4 引入的补列做存在性迁移（#11 教训：先 PRAGMA 查列、
// 缺则 ALTER ADD，绝不放进 CREATE TABLE IF NOT EXISTS 同批 DDL）。
// classes.teacher_passcode：M6 起口令体系废弃，仅保留列不回填；
// point_logs.operator：新库默认 'teacher'，存量流水保留原值；
// point_logs.request_id：idx_point_logs_dedupe 引用该列，M1 老库缺列时
// 直升 M6 若不先补列则启动即崩（对抗审查 P1 复现）。
func migrateM4Columns(db *sql.DB) error {
	hasRequestID, err := columnExists(db, "point_logs", "request_id")
	if err != nil {
		return err
	}
	if !hasRequestID {
		if _, err := db.Exec(`ALTER TABLE point_logs ADD COLUMN request_id TEXT`); err != nil {
			return fmt.Errorf("migrate: add point_logs.request_id: %w", err)
		}
	}
	hasPasscode, err := columnExists(db, "classes", "teacher_passcode")
	if err != nil {
		return err
	}
	if !hasPasscode {
		if _, err := db.Exec(`ALTER TABLE classes ADD COLUMN teacher_passcode TEXT`); err != nil {
			return fmt.Errorf("migrate: add classes.teacher_passcode: %w", err)
		}
	}
	hasOperator, err := columnExists(db, "point_logs", "operator")
	if err != nil {
		return err
	}
	if !hasOperator {
		if _, err := db.Exec(`ALTER TABLE point_logs ADD COLUMN operator TEXT NOT NULL DEFAULT 'teacher'`); err != nil {
			return fmt.Errorf("migrate: add point_logs.operator: %w", err)
		}
	}
	// M12（#51）：积分经济。pets.currency 存量从 0 起算（公告口径"积分自
	// 上线起累计"，不迁移）；active_scene 当前展示皮肤（NULL=默认无皮肤）；
	// point_logs.type 区分 earn（加分）/spend（购皮肤），存量流水一律 earn。
	for _, m := range []struct{ table, column, ddl string }{
		{"pets", "currency", `ALTER TABLE pets ADD COLUMN currency INTEGER NOT NULL DEFAULT 0`},
		{"pets", "active_scene", `ALTER TABLE pets ADD COLUMN active_scene TEXT`},
		{"point_logs", "type", `ALTER TABLE point_logs ADD COLUMN type TEXT NOT NULL DEFAULT 'earn'`},
	} {
		has, err := columnExists(db, m.table, m.column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := db.Exec(m.ddl); err != nil {
				return fmt.Errorf("migrate: add %s.%s: %w", m.table, m.column, err)
			}
		}
	}
	return nil
}

// rebuildStudents 保证 students 表为 M6 目标 schema（含 deleted_at，唯一性走
// 部分唯一索引）。SQLite 无法原地修改 UNIQUE 约束，老库（无 deleted_at）按
// 官方表重建流程的等价序列执行：
//
//	PRAGMA foreign_keys=OFF（事务外）→ 建 students_new → 拷贝 → DROP 旧表
//	→ RENAME → 事务提交 → PRAGMA foreign_keys=ON（事务外）
//
// 外键引用（pets.student_id 等）跟随表名，RENAME 后自动指向新表。
func rebuildStudents(db *sql.DB) error {
	hasStudents, err := tableExists(db, "students")
	if err != nil {
		return err
	}
	if !hasStudents {
		// 新装：直接建目标 schema
		const fresh = `
CREATE TABLE students (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	class_id   INTEGER NOT NULL REFERENCES classes(id),
	name       TEXT NOT NULL,
	student_no TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	deleted_at DATETIME
);`
		if _, err := db.Exec(fresh); err != nil {
			return fmt.Errorf("migrate: create students: %w", err)
		}
		return nil
	}
	hasDeletedAt, err := columnExists(db, "students", "deleted_at")
	if err != nil {
		return err
	}
	if hasDeletedAt {
		return nil // 已是目标 schema（重启幂等）
	}

	// 老库重建。foreign_keys 开关必须在事务外执行（SQLite 事务内为 no-op）。
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("migrate: fk off: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const rebuild = `
CREATE TABLE students_new (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	class_id   INTEGER NOT NULL REFERENCES classes(id),
	name       TEXT NOT NULL,
	student_no TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	deleted_at DATETIME
);
INSERT INTO students_new (id, class_id, name, student_no, created_at, deleted_at)
	SELECT id, class_id, name, student_no, created_at, NULL FROM students;
DROP TABLE students;
ALTER TABLE students_new RENAME TO students;
`
	if _, err := tx.Exec(rebuild); err != nil {
		return fmt.Errorf("migrate: rebuild students: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("migrate: fk on: %w", err)
	}
	return nil
}

// tableExists 判断表是否存在。
func tableExists(db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
	).Scan(&n)
	return n > 0, err
}

// columnExists 用 pragma table_info 判断列是否存在（table 为代码内常量，非外部输入）。
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notNull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migrateTeacherPassVer 为 #53 教师自助改密补列（#11 教训：PRAGMA 查列、
// 缺则 ALTER ADD，绝不放进 CREATE TABLE IF NOT EXISTS 同批 DDL）。
// pass_ver 是教师密码版本：改密递增，签发 token 携带签发时版本，
// requireTeacher 比对不一致即 401（旧会话全失效）。存量教师默认 0。
func migrateTeacherPassVer(db *sql.DB) error {
	has, err := columnExists(db, "teachers", "pass_ver")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE teachers ADD COLUMN pass_ver INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate: add teachers.pass_ver: %w", err)
		}
	}
	return nil
}
