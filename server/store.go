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
	operator   TEXT NOT NULL DEFAULT 'student',
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`
	// point_logs 的索引不能与建表同批执行：M1 老库的该表已存在（无 request_id 列），
	// 建表会被跳过而索引会因缺列崩溃（issue #11）。必须先补列、再建索引。
	if _, err := db.Exec(ddl); err != nil {
		return err
	}
	hasRequestID, err := columnExists(db, "point_logs", "request_id")
	if err != nil {
		return err
	}
	if !hasRequestID {
		if _, err := db.Exec(`ALTER TABLE point_logs ADD COLUMN request_id TEXT`); err != nil {
			return fmt.Errorf("migrate: add point_logs.request_id: %w", err)
		}
	}
	const idx = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_point_logs_dedupe
	ON point_logs (pet_id, request_id) WHERE request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_point_logs_pet ON point_logs (pet_id, id);
`
	if _, err := db.Exec(idx); err != nil {
		return err
	}
	return migrateM4Columns(db)
}

// migrateM4Columns 为 M4 教师端补列（issue #11 教训：先 PRAGMA 查列、缺则 ALTER ADD，
// 绝不把新列引用放进 CREATE TABLE IF NOT EXISTS 同批 DDL）。
// classes.teacher_passcode：存量班级回填随机口令；point_logs.operator：存量流水默认 'student'。
func migrateM4Columns(db *sql.DB) error {
	hasPasscode, err := columnExists(db, "classes", "teacher_passcode")
	if err != nil {
		return err
	}
	if !hasPasscode {
		if _, err := db.Exec(`ALTER TABLE classes ADD COLUMN teacher_passcode TEXT`); err != nil {
			return fmt.Errorf("migrate: add classes.teacher_passcode: %w", err)
		}
	}
	if err := backfillTeacherPasscodes(db); err != nil {
		return err
	}

	hasOperator, err := columnExists(db, "point_logs", "operator")
	if err != nil {
		return err
	}
	if !hasOperator {
		if _, err := db.Exec(`ALTER TABLE point_logs ADD COLUMN operator TEXT NOT NULL DEFAULT 'student'`); err != nil {
			return fmt.Errorf("migrate: add point_logs.operator: %w", err)
		}
	}
	return nil
}

// backfillTeacherPasscodes 为存量班级补随机口令（每班独立随机，逐行回填）。
func backfillTeacherPasscodes(db *sql.DB) error {
	rows, err := db.Query(`SELECT id FROM classes WHERE teacher_passcode IS NULL OR teacher_passcode = ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE classes SET teacher_passcode = ? WHERE id = ?`, generatePasscode(), id); err != nil {
			return err
		}
	}
	return nil
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
