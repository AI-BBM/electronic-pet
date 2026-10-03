package server_test

// M6 纯教师侧改造（issue #25）测试公共设施。
//
// 黑盒口径与既有各里程碑一致：只经 server.New(dbPath) 构建的 handler 发真实 HTTP；
// 唯一例外是任务书明确允许的「直查/直写 DB」场景（读验证码、构造 M5 老库、
// 断言迁移结果、构造清理任务所需的过期 deleted_at 数据），一律用第二连接完成。
//
// 依赖与隔离说明：
//   - 仅复用 helpers_test.go 的黑盒核心（newHandler/newHandlerAt/performJSON/doJSON/
//     expectError）；不使用任何学生端 helper（mustJoin/adoptEgg/eggIDs 等，
//     M6 下线后这些将被处置，本文件不得反向依赖它们）。
//   - modernc.org/sqlite 驱动的 blank import 已由包内既有文件（teacher_test.go）完成，
//     本文件不重复 import，直接 sql.Open("sqlite", ...)。若实现阶段删除了包内全部
//     blank import 文件，须把 `_ "modernc.org/sqlite"` 移入本文件（处置清单已注明）。
//   - 验证码全链路依赖「未配置 SMTP_HOST → mock 发信」口径：跑本测试的环境不得设置
//     SMTP_HOST 环境变量（go test 进程默认继承宿主环境，CI 需保证无此变量）。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // sqlite 驱动注册（原由 teacher_test.go 持有，处置后归本文件）

	"github.com/AI-BBM/electronic-pet/server"
)

// ---------- 基础设施 ----------

// m6NewAt 在指定 dbPath 上构建被测 handler（迁移/重启/清理场景复用同一 DB 文件），
// 测试结束自动 Close 释放 SQLite 句柄（Windows 文件锁）。
func m6NewAt(t *testing.T, dbPath string) http.Handler {
	t.Helper()
	h, err := server.New(dbPath)
	if err != nil {
		t.Fatalf("server.New(%q) 返回错误: %v", dbPath, err)
	}
	t.Cleanup(func() {
		if c, ok := h.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	})
	return h
}

// m6Handler 在独立临时目录构建被测 handler，并返回 dbPath（直查 DB 需要）。
func m6Handler(t *testing.T) (http.Handler, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "m6.db")
	return m6NewAt(t, dbPath), dbPath
}

// m6OpenDB 以第二连接打开 DB（busy_timeout 防与被测 handler 争锁；WAL 由被测方启用）。
func m6OpenDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开 DB %s 失败: %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// m6Do 发送 JSON 请求，返回状态码、解码后的 body（非 JSON 时为 nil）与原始响应体。
func m6Do(h http.Handler, method, target, token string, body any) (int, map[string]any, []byte) {
	resp := performJSON(h, method, target, token, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded, raw
}

// m6Status 仅返回状态码（404/405 探测；响应体可能是 HTML，不要求 JSON）。
func m6Status(h http.Handler, method, target, token string, body any) int {
	resp := performJSON(h, method, target, token, body)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// readAll 读取并关闭响应体（需要同时拿响应头与原始响应体的场景）。
func readAll(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return raw
}

// m6BadToken 把合法 token 末位替换成非 base64url 字符（必然解码失败 → 401）。
// 与 m2TamperToken 同理：不能用字母表内字符（末位仅 4 个有效位，A/B/C/D 同解码）。
func m6BadToken(token string) string {
	if token == "" {
		return "not-a-token"
	}
	return token[:len(token)-1] + "!"
}

// ---------- 教师账号（注册 / 登录 / 发码） ----------

// m6Password 是注册 helper 使用的合规密码（≥8 字符）。
const m6Password = "teacher-pass-123"

// m6CodeRe 钉死验证码形态：恰好 6 位数字。
var m6CodeRe = regexp.MustCompile(`^\d{6}$`)

// m6UnixRe 匹配纯数字串（deleted_at 以 unix 秒存储时的 CAST 表示）。
var m6UnixRe = regexp.MustCompile(`^\d+$`)

// m6EmailCode 调 POST /api/teacher/email-code（免鉴权）。
func m6EmailCode(h http.Handler, email string) (int, map[string]any) {
	status, body, _ := m6Do(h, http.MethodPost, "/api/teacher/email-code", "",
		map[string]string{"email": email})
	return status, body
}

// m6ReadEmailCode 直查 meta 表读取验证码，并断言约定行为：
// 键 email_code:<email> 存在、值格式 <code>|<expiresUnix>、code 为 6 位数字。
func m6ReadEmailCode(t *testing.T, dbPath, email string) string {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	var v string
	if err := db.QueryRow(`SELECT v FROM meta WHERE k = ?`, "email_code:"+email).Scan(&v); err != nil {
		t.Fatalf("meta 表缺少 email_code:%s（mock 发信必须按约定键落库）: %v", email, err)
	}
	code, _, ok := strings.Cut(v, "|")
	if !ok {
		t.Fatalf("email_code:%s 的值 %q 不符合 <code>|<expiresUnix> 约定", email, v)
	}
	if !m6CodeRe.MatchString(code) {
		t.Fatalf("email_code:%s 的码部分 = %q, 期望 6 位数字", email, code)
	}
	return code
}

// m6WriteEmailCode 按约定格式直写一条有效验证码（构造确定性注册场景：
// 已注册邮箱的 409 用例需要一条「码正确」的记录，而发码接口受 60s 限速无法重发）。
func m6WriteEmailCode(t *testing.T, dbPath, email, code string, expires time.Time) {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	v := fmt.Sprintf("%s|%d", code, expires.Unix())
	if _, err := db.Exec(
		`INSERT INTO meta(k, v) VALUES(?, ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		"email_code:"+email, v,
	); err != nil {
		t.Fatalf("直写 email_code:%s 失败: %v", email, err)
	}
}

// m6ExpireEmailCode 把已发验证码的过期时间改到过去（保留码本身，格式其余部分原样）。
func m6ExpireEmailCode(t *testing.T, dbPath, email string, expiredAt time.Time) {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	var v string
	if err := db.QueryRow(`SELECT v FROM meta WHERE k = ?`, "email_code:"+email).Scan(&v); err != nil {
		t.Fatalf("读取 email_code:%s 失败: %v", email, err)
	}
	parts := strings.Split(v, "|")
	if len(parts) < 2 {
		t.Fatalf("email_code:%s 的值 %q 不符合 <code>|<expiresUnix> 约定", email, v)
	}
	parts[1] = strconv.FormatInt(expiredAt.Unix(), 10)
	if _, err := db.Exec(`UPDATE meta SET v = ? WHERE k = ?`, strings.Join(parts, "|"), "email_code:"+email); err != nil {
		t.Fatalf("改写 email_code:%s 过期时间失败: %v", email, err)
	}
}

// m6Register 调 POST /api/teacher/register（验证码字段名按任务书钉死为 code）。
func m6Register(h http.Handler, email, code, password, className string) (int, map[string]any) {
	status, body, _ := m6Do(h, http.MethodPost, "/api/teacher/register", "", map[string]string{
		"email":     email,
		"code":      code,
		"password":  password,
		"className": className,
	})
	return status, body
}

// m6RegisterTeacher 走「发码 → 直查 DB 取码 → 注册」全链路，断言成功并返回教师 token。
func m6RegisterTeacher(t *testing.T, h http.Handler, dbPath, email, className string) string {
	t.Helper()
	if status, body := m6EmailCode(h, email); status != http.StatusOK {
		t.Fatalf("发码(%s) 状态码 = %d, 期望 200; body=%v", email, status, body)
	}
	code := m6ReadEmailCode(t, dbPath, email)
	status, body := m6Register(h, email, code, m6Password, className)
	if status != http.StatusOK {
		t.Fatalf("注册(%s) 状态码 = %d, 期望 200; body=%v", email, status, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("注册(%s) 未返回 token: %v", email, body)
	}
	return token
}

// m6Login 调 POST /api/teacher/login（M6 邮箱式登录）。
func m6Login(h http.Handler, email, password string) (int, map[string]any) {
	status, body, _ := m6Do(h, http.MethodPost, "/api/teacher/login", "", map[string]string{
		"email":    email,
		"password": password,
	})
	return status, body
}

// m6WrongCode 由正确码确定性推导一个不同的 6 位数字码。
func m6WrongCode(code string) string {
	b := []byte(code)
	if len(b) == 0 {
		return "000000"
	}
	if b[0] == '9' {
		b[0] = '0'
	} else {
		b[0]++
	}
	return string(b)
}

// ---------- 名单 CRUD / 垃圾桶 ----------

// m6Student 是名单对象的最小契约字段集（增/改/恢复的响应信封 {"student":{...}}）。
type m6Student struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	StudentNo string `json:"studentNo"`
}

// m6TrashItem 是 GET /api/teacher/trash 单条记录的最小契约字段集。
type m6TrashItem struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	StudentNo string `json:"studentNo"`
	DeletedAt string `json:"deletedAt"`
}

// m6RosterEntry 是花名册单行的最小断言字段集（沿用 M4 形状的子集）。
type m6RosterEntry struct {
	StudentNo string `json:"studentNo"`
	Name      string `json:"name"`
	Adopted   bool   `json:"adopted"`
	PetName   string `json:"petName"`
	Points    int    `json:"points"`
}

// m6StudentPath 拼名单单生路径（改/删/恢复）。
func m6StudentPath(id int64, suffix string) string {
	if suffix == "" {
		return fmt.Sprintf("/api/teacher/students/%d", id)
	}
	return fmt.Sprintf("/api/teacher/students/%d/%s", id, suffix)
}

// m6CreateStudent 调 POST /api/teacher/students。
func m6CreateStudent(h http.Handler, token, name, studentNo string) (int, m6Student, map[string]any) {
	status, body, raw := m6Do(h, http.MethodPost, "/api/teacher/students", token,
		map[string]string{"name": name, "studentNo": studentNo})
	var env struct {
		Student m6Student `json:"student"`
	}
	_ = json.Unmarshal(raw, &env)
	return status, env.Student, body
}

// m6MustCreateStudent 断言新增成功并返回学生（含 id）。
func m6MustCreateStudent(t *testing.T, h http.Handler, token, name, studentNo string) m6Student {
	t.Helper()
	status, st, body := m6CreateStudent(h, token, name, studentNo)
	if status != http.StatusOK {
		t.Fatalf("新增学生(%s/%s) 状态码 = %d, 期望 200; body=%v", name, studentNo, status, body)
	}
	if st.ID <= 0 {
		t.Fatalf("新增学生(%s/%s) 未返回有效 id: %v", name, studentNo, body)
	}
	if st.Name != name || st.StudentNo != studentNo {
		t.Fatalf("新增学生响应 = %+v, 期望 name=%q studentNo=%q", st, name, studentNo)
	}
	return st
}

// m6PatchStudent 调 PATCH /api/teacher/students/{id}（body 为 nil 时表示空对象）。
func m6PatchStudent(h http.Handler, token string, id int64, fields map[string]string) (int, m6Student, map[string]any) {
	if fields == nil {
		fields = map[string]string{}
	}
	status, body, raw := m6Do(h, http.MethodPatch, m6StudentPath(id, ""), token, fields)
	var env struct {
		Student m6Student `json:"student"`
	}
	_ = json.Unmarshal(raw, &env)
	return status, env.Student, body
}

// m6DeleteStudent 调 DELETE /api/teacher/students/{id}（软删入垃圾桶）。
func m6DeleteStudent(h http.Handler, token string, id int64) (int, map[string]any) {
	status, body, _ := m6Do(h, http.MethodDelete, m6StudentPath(id, ""), token, nil)
	return status, body
}

// m6RestoreStudent 调 POST /api/teacher/students/{id}/restore。
func m6RestoreStudent(h http.Handler, token string, id int64) (int, m6Student, map[string]any) {
	status, body, raw := m6Do(h, http.MethodPost, m6StudentPath(id, "restore"), token, nil)
	var env struct {
		Student m6Student `json:"student"`
	}
	_ = json.Unmarshal(raw, &env)
	return status, env.Student, body
}

// m6TrashList 调 GET /api/teacher/trash，解码 {items:[...]}。
func m6TrashList(h http.Handler, token string) (int, []m6TrashItem, map[string]any) {
	status, body, raw := m6Do(h, http.MethodGet, "/api/teacher/trash", token, nil)
	var env struct {
		Items []m6TrashItem `json:"items"`
	}
	_ = json.Unmarshal(raw, &env)
	return status, env.Items, body
}

// m6Roster 调 GET /api/teacher/roster，解码 {students:[...]}（沿用 M4 形状）。
func m6Roster(h http.Handler, token string) (int, []m6RosterEntry, map[string]any) {
	status, body, raw := m6Do(h, http.MethodGet, "/api/teacher/roster", token, nil)
	var env struct {
		Students []m6RosterEntry `json:"students"`
	}
	_ = json.Unmarshal(raw, &env)
	return status, env.Students, body
}

// ---------- 沿用端点（代领 / 代加分 / 改宠物名） ----------

// m6PetView 是 pet 对象的最小断言字段集（M1/M4 形状子集）。
type m6PetView struct {
	Name    string `json:"name"`
	Species struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"species"`
	Level           int  `json:"level"`
	Points          int  `json:"points"`
	NextLevelPoints *int `json:"nextLevelPoints"`
}

// m6AdoptFor 调 POST /api/teacher/adopt（代学生领蛋，种类随机）。
func m6AdoptFor(h http.Handler, token, studentNo string) (int, m6PetView, map[string]any) {
	status, body, raw := m6Do(h, http.MethodPost, "/api/teacher/adopt", token,
		map[string]string{"studentNo": studentNo})
	var env struct {
		Pet m6PetView `json:"pet"`
	}
	_ = json.Unmarshal(raw, &env)
	return status, env.Pet, body
}

// m6PointsReq 是 POST /api/teacher/points 的请求体形状。
type m6PointsReq struct {
	StudentNo string `json:"studentNo"`
	Reason    string `json:"reason"`
	Value     int    `json:"value"`
	RequestID string `json:"requestId,omitempty"`
}

// m6AddPointsFor 调 POST /api/teacher/points 并解码 pet 信封与 added。
func m6AddPointsFor(h http.Handler, token string, req m6PointsReq) (int, m6PetView, bool, map[string]any) {
	status, body, raw := m6Do(h, http.MethodPost, "/api/teacher/points", token, req)
	var env struct {
		Pet   m6PetView `json:"pet"`
		Added bool      `json:"added"`
	}
	_ = json.Unmarshal(raw, &env)
	return status, env.Pet, env.Added, body
}

// m6RenamePet 调 POST /api/teacher/pets/{studentID}/name（教师改宠物名，可多次）。
func m6RenamePet(h http.Handler, token string, studentID int64, name string) (int, m6PetView, map[string]any) {
	status, body, raw := m6Do(h, http.MethodPost,
		fmt.Sprintf("/api/teacher/pets/%d/name", studentID), token,
		map[string]string{"name": name})
	var env struct {
		Pet m6PetView `json:"pet"`
	}
	_ = json.Unmarshal(raw, &env)
	return status, env.Pet, body
}

// ---------- 教师 token 直签（存量班级无账号场景的测试口径） ----------

// m6SignTeacherToken 按 auth 契约（payload "T<classID>.<nonce>" + HMAC-SHA256，
// 两段均 base64url）签发教师 token。用于给「迁移而来的存量班级」构造教师身份——
// 存量班级无教师账号（PRD：管理员脚本开通，不在本 Issue），黑盒接口拿不到其 token，
// 而 HMAC 密钥持久化在 meta（key=token_secret）可直查，签名格式由契约钉死。
func m6SignTeacherToken(secret string, classID int64) string {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	payload := fmt.Sprintf("T%d.%s", classID, base64.RawURLEncoding.EncodeToString(nonce))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// m6TeacherTokenForClass 读 meta.token_secret 并为指定班级签发教师 token。
func m6TeacherTokenForClass(t *testing.T, dbPath string, classID int64) string {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	var secret string
	if err := db.QueryRow(`SELECT v FROM meta WHERE k = 'token_secret'`).Scan(&secret); err != nil {
		t.Fatalf("读取 token_secret 失败: %v", err)
	}
	return m6SignTeacherToken(secret, classID)
}

// m6ClassIDByCode 按班级码查班级 id（seed 老库后取存量班级）。
func m6ClassIDByCode(t *testing.T, dbPath, code string) int64 {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	var id int64
	if err := db.QueryRow(`SELECT id FROM classes WHERE code = ?`, code).Scan(&id); err != nil {
		t.Fatalf("查询班级 %s 失败: %v", code, err)
	}
	return id
}

// ---------- M5 老库构造（#11 教训流程：直建旧 schema → New() 升级） ----------

// m6M5OldDDL 是 M5 版本（M6 之前、即 main 现行 store.go）的建库语句：
// classes 含 teacher_passcode、point_logs 含 request_id/operator 与索引、
// students 带表内 UNIQUE(class_id, student_no) 且无 deleted_at。
const m6M5OldDDL = `
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
CREATE UNIQUE INDEX IF NOT EXISTS idx_point_logs_dedupe
	ON point_logs (pet_id, request_id) WHERE request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_point_logs_pet ON point_logs (pet_id, id);
`

// m6SeedM5OldDB 用 M5 旧 DDL 建库并预置数据：一班（m6old）两学生——
// S001 有宠物（19 分）与一条老流水，S002 无宠。
func m6SeedM5OldDB(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(m6M5OldDDL); err != nil {
		t.Fatalf("执行 M5 旧 DDL 失败: %v", err)
	}
	seed := `
INSERT INTO classes (code, teacher_passcode) VALUES ('m6old', 'legacy-passcode');
INSERT INTO students (class_id, name, student_no)
	SELECT id, '老同学甲', 'S001' FROM classes WHERE code = 'm6old';
INSERT INTO students (class_id, name, student_no)
	SELECT id, '老同学乙', 'S002' FROM classes WHERE code = 'm6old';
INSERT INTO pets (student_id, species_id, name, level, points, egg_id)
	SELECT id, 'cat', '老猫', 1, 19, 'egg-1' FROM students WHERE student_no = 'S001';
INSERT INTO point_logs (pet_id, delta, reason, request_id, operator)
	SELECT id, 19, '入学礼包', 'legacy-1', 'student' FROM pets WHERE name = '老猫';
`
	if _, err := db.Exec(seed); err != nil {
		t.Fatalf("seed 旧库数据失败: %v", err)
	}
}

// ---------- 通用 DB 断言小件 ----------

// m6Count 执行 COUNT 标量查询。
func m6Count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("计数查询 %q 失败: %v", query, err)
	}
	return n
}

// m6HasColumn 用 PRAGMA table_info 判断列是否存在（不存在不算失败，由调用方断言）。
func m6HasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) 失败: %v", table, err)
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
			t.Fatalf("扫描 table_info(%s) 失败: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 table_info(%s) 失败: %v", table, err)
	}
	return false
}

// m6HasIndex 在 sqlite_master 中判断索引是否存在。
func m6HasIndex(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name,
	).Scan(&n); err != nil {
		t.Fatalf("查询 sqlite_master 失败: %v", err)
	}
	return n > 0
}

// m6ShiftDeletedAt 把某学生的 deleted_at 平移 delta（清理任务构造 100/89 天前数据）。
// 存储格式由实现定（unix 秒或 SQLite datetime 字符串），此处读原值按原格式回写，
// 两种主流格式均兼容；均解析失败时 Fatal 提示实现方对齐格式。
func m6ShiftDeletedAt(t *testing.T, db *sql.DB, studentID int64, delta time.Duration) {
	t.Helper()
	var raw string
	if err := db.QueryRow(
		`SELECT CAST(deleted_at AS TEXT) FROM students WHERE id = ?`, studentID,
	).Scan(&raw); err != nil {
		t.Fatalf("读取学生 %d 的 deleted_at 失败: %v", studentID, err)
	}
	if raw == "" {
		t.Fatalf("学生 %d 的 deleted_at 为空（DELETE 必须先写入删除时间）", studentID)
	}
	var newVal string
	if m6UnixRe.MatchString(raw) { // unix 秒（整数存储经 CAST 变纯数字串）
		unix, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatalf("deleted_at=%q 解析 unix 秒失败: %v", raw, err)
		}
		newVal = strconv.FormatInt(unix+int64(delta/time.Second), 10)
	} else { // datetime 字符串：按常见布局逐个尝试，成功者原布局回写
		layouts := []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05"}
		parsed := false
		for _, lay := range layouts {
			if ts, err := time.Parse(lay, raw); err == nil {
				newVal = ts.Add(delta).Format(lay)
				parsed = true
				break
			}
		}
		if !parsed {
			t.Fatalf("deleted_at 存储格式 %q 无法解析（仅支持 unix 秒或 SQLite datetime/RFC3339 字符串）", raw)
		}
	}
	if _, err := db.Exec(`UPDATE students SET deleted_at = ? WHERE id = ?`, newVal, studentID); err != nil {
		t.Fatalf("回写学生 %d 的 deleted_at 失败: %v", studentID, err)
	}
}
