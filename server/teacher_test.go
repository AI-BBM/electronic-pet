package server_test

// M4 教师端黑盒用例（测试先行）——本文件含 t4 公共设施与：
//   T1 结构完整性 / T2 教师登录 / T3 角色双向隔离 / T8 方法限制(405)。
// T4 代领、T5 教师加分、T7 改密见 teacher_actions_test.go；
// T6 花名册见 teacher_roster_test.go；T9/T10 迁移回归见 teacher_migrate_test.go。
// 契约来源：docs/product/features/m4-teacher.md + M4 测试先行任务书。
//
// 教师密码由建班时随机生成、不提供查询接口，故测试经 database/sql 直读/直写 DB
// 获取或设定（黑盒 HTTP 之外唯一允许的直连 DB 场景，与迁移断言一致）。

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // 测试直连 DB：教师密码读取/设定
)

// ---------- t4 公共设施（teacher 系列测试共用） ----------

// t4Handler 构建被测 handler 并返回其 DB 路径（教师密码直读/直写、迁移断言需要）。
func t4Handler(t *testing.T) (http.Handler, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pet.db")
	return newHandlerAt(t, dbPath), dbPath
}

// t4OpenDB 以第二连接打开 DB（busy_timeout 防与被测 handler 争锁；WAL 由被测方启用）。
func t4OpenDB(t *testing.T, dbPath string) *sql.DB {
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

// t4GetPasscode 读取班级当前教师密码（建班时随机生成；前提：班级已存在）。
func t4GetPasscode(t *testing.T, dbPath, classCode string) string {
	t.Helper()
	db := t4OpenDB(t, dbPath)
	var pass string
	err := db.QueryRow(`SELECT teacher_passcode FROM classes WHERE code = ?`, classCode).Scan(&pass)
	if err != nil {
		t.Fatalf("读取班级 %s 的 teacher_passcode 失败（列缺失或未生成?）: %v", classCode, err)
	}
	return pass
}

// t4SetPasscode 直接把班级教师密码置为已知值（构造确定性场景；前提：班级已存在）。
func t4SetPasscode(t *testing.T, dbPath, classCode, passcode string) {
	t.Helper()
	db := t4OpenDB(t, dbPath)
	res, err := db.Exec(`UPDATE classes SET teacher_passcode = ? WHERE code = ?`, passcode, classCode)
	if err != nil {
		t.Fatalf("更新班级 %s 的 teacher_passcode 失败（列缺失?）: %v", classCode, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("更新 teacher_passcode 影响 %d 行, 期望 1（班级 %s 不存在?）", n, classCode)
	}
}

// t4LoginResult 是 POST /api/teacher/login 的结果快照。
type t4LoginResult struct {
	Status int
	Token  string
	Body   map[string]any
}

// t4Login 调 POST /api/teacher/login（登录口，不携带任何 Authorization 头）。
func t4Login(h http.Handler, classCode, passcode string) t4LoginResult {
	resp := performJSON(h, http.MethodPost, "/api/teacher/login", "", map[string]string{
		"classCode":       classCode,
		"teacherPasscode": passcode,
	})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	token, _ := body["token"].(string)
	return t4LoginResult{Status: resp.StatusCode, Token: token, Body: body}
}

// t4MustLogin 断言登录成功（200 + 非空 token）并返回教师 token。
func t4MustLogin(t *testing.T, h http.Handler, classCode, passcode string) string {
	t.Helper()
	res := t4Login(h, classCode, passcode)
	if res.Status != http.StatusOK {
		t.Fatalf("teacher login(%s) 状态码 = %d, 期望 200; body=%v", classCode, res.Status, res.Body)
	}
	if res.Token == "" {
		t.Fatalf("teacher login(%s) 未返回 token; body=%v", classCode, res.Body)
	}
	return res.Token
}

// t4TeacherToken 前提：班级已存在（至少一名学生 join 过，由调用方保证）。
// 从 DB 读建班时随机生成的教师密码并登录，返回教师 token。
func t4TeacherToken(t *testing.T, h http.Handler, dbPath, classCode string) string {
	t.Helper()
	return t4MustLogin(t, h, classCode, t4GetPasscode(t, dbPath, classCode))
}

// t4RawRequest 发送原始字符串请求体（构造非法 JSON 等），返回状态码与原始响应体。
func t4RawRequest(h http.Handler, method, target, token, rawBody string) (int, []byte) {
	req := httptest.NewRequest(method, target, strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// t4AdoptResult 是 POST /api/teacher/adopt 的结果快照。
type t4AdoptResult struct {
	Status int
	Body   map[string]any
	Pet    pet
}

// t4Adopt 教师代学生领蛋孵化（body 仅 studentNo，种类随机走 M1 规则）。
func t4Adopt(h http.Handler, teacherToken, studentNo string) t4AdoptResult {
	resp := performJSON(h, http.MethodPost, "/api/teacher/adopt", teacherToken,
		map[string]string{"studentNo": studentNo})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	res := t4AdoptResult{Status: resp.StatusCode, Body: body}
	var env struct {
		Pet pet `json:"pet"`
	}
	if json.Unmarshal(raw, &env) == nil {
		res.Pet = env.Pet
	}
	return res
}

// t4PointsRequest 是 POST /api/teacher/points 的请求体形状。
type t4PointsRequest struct {
	StudentNo string `json:"studentNo"`
	Reason    string `json:"reason"`
	Value     int    `json:"value"`
	RequestID string `json:"requestId,omitempty"`
}

// t4PointsResult 是一次教师加分请求的解码结果。
type t4PointsResult struct {
	Status  int
	Body    map[string]any
	Pet     pet
	LevelUp bool
	Level   int
	Added   bool
}

// t4AddPoints 教师给学生加分并解码响应。
func t4AddPoints(h http.Handler, teacherToken string, req t4PointsRequest) t4PointsResult {
	resp := performJSON(h, http.MethodPost, "/api/teacher/points", teacherToken, req)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	res := t4PointsResult{Status: resp.StatusCode, Body: body}
	var decoded struct {
		Pet     pet  `json:"pet"`
		LevelUp bool `json:"levelUp"`
		Level   int  `json:"level"`
		Added   bool `json:"added"`
	}
	if json.Unmarshal(raw, &decoded) == nil {
		res.Pet, res.LevelUp, res.Level, res.Added = decoded.Pet, decoded.LevelUp, decoded.Level, decoded.Added
	}
	return res
}

// t4RosterEntry 是 GET /api/teacher/roster 中单个学生的契约形状（M4 定稿字段集）。
type t4RosterEntry struct {
	StudentNo    string `json:"studentNo"`
	Name         string `json:"name"`
	Adopted      bool   `json:"adopted"`
	SpeciesID    string `json:"speciesId"`
	SpeciesName  string `json:"speciesName"`
	Level        int    `json:"level"`
	Points       int    `json:"points"`
	LastPointsAt string `json:"lastPointsAt"`
}

// t4RosterResult 是 GET /api/teacher/roster 的结果快照。
type t4RosterResult struct {
	Status   int
	Body     map[string]any
	Students []t4RosterEntry
}

// t4Roster 拉取班级花名册（教师 token）。
func t4Roster(h http.Handler, teacherToken string) t4RosterResult {
	resp := performJSON(h, http.MethodGet, "/api/teacher/roster", teacherToken, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	var decoded struct {
		Students []t4RosterEntry `json:"students"`
	}
	_ = json.Unmarshal(raw, &decoded)
	return t4RosterResult{Status: resp.StatusCode, Body: body, Students: decoded.Students}
}

// t4Passcode 调 POST /api/teacher/passcode 修改教师密码，返回状态码与解码 body。
func t4Passcode(h http.Handler, teacherToken, oldPasscode, newPasscode string) (int, map[string]any) {
	resp := performJSON(h, http.MethodPost, "/api/teacher/passcode", teacherToken, map[string]string{
		"oldPasscode": oldPasscode,
		"newPasscode": newPasscode,
	})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

// t4LogItem 是 M4 之后流水单条记录的最小字段集（新增 operator）。
type t4LogItem struct {
	ID        int64  `json:"id"`
	Value     int    `json:"value"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
	Operator  string `json:"operator"`
}

// t4LogList 是 GET /api/pet/me/log 的响应体形状（分页语义不变，pageSize 固定 20）。
type t4LogList struct {
	Items    []t4LogItem `json:"items"`
	Page     int         `json:"page"`
	PageSize int         `json:"pageSize"`
	Total    int         `json:"total"`
}

// t4GetLog 调 GET /api/pet/me/log（学生 token）并解码。
func t4GetLog(t *testing.T, h http.Handler, studentToken, query string) t4LogList {
	t.Helper()
	resp := performJSON(h, http.MethodGet, "/api/pet/me/log"+query, studentToken, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var list t4LogList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("log 响应解码失败 (%s): %v", raw, err)
	}
	return list
}

// ---------- T1 结构完整性 ----------

// T1 结构完整性：M4 契约要求的实现/页面文件必须存在。
// 命令: go test ./server/ -run TestT4Structure_Files -v
// 预期: 实现落地前 FAIL（缺少 server/teacher.go 与 web/static/teacher.html）；落地后 PASS。
func TestT4Structure_Files(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 无法定位测试文件所在目录")
	}
	root := filepath.Dir(filepath.Dir(thisFile)) // server/ 的上一级即仓库根
	for _, name := range []string{
		"server/teacher.go",
		"web/static/teacher.html",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Errorf("缺少实现文件 %s: %v", name, err)
		}
	}
}

// ---------- T2 教师登录 ----------

// T2 登录：班级码+教师密码 → 200+token（token 可调教师端点）；
// 班级不存在 404；密码错 401；字段空白 400；登录口无需任何 token（t4Login 从不携带 Authorization）。
// 命令: go test ./server/ -run TestT4Login -v
// 预期: 实现落地前 FAIL（/api/teacher/login 未注册 → 404/非 JSON）；落地后 PASS。
func TestT4Login(t *testing.T) {
	h, dbPath := t4Handler(t)
	mustJoin(t, h, "c201", "登录班学生", "S001") // 首个 join 建班
	pass := t4GetPasscode(t, dbPath, "c201")

	// 成功：200 + 非空 token，且该 token 能调教师端点
	token := t4MustLogin(t, h, "c201", pass)
	if roster := t4Roster(h, token); roster.Status != http.StatusOK {
		t.Errorf("登录所得 token 调 GET /api/teacher/roster 状态码 = %d, 期望 200; body=%v",
			roster.Status, roster.Body)
	}

	// 班级不存在 → 404 + error
	res := t4Login(h, "no-such-class", pass)
	expectError(t, res.Status, res.Body, http.StatusNotFound, "登录班级不存在")

	// 密码错 → 401 + error
	res = t4Login(h, "c201", "definitely-wrong-passcode")
	expectError(t, res.Status, res.Body, http.StatusUnauthorized, "教师密码错误")

	// 字段空白（缺失即空串/纯空白）→ 400
	for i, tc := range []struct{ classCode, passcode string }{
		{"", pass},
		{"   ", pass},
		{"c201", ""},
		{"c201", "   "},
	} {
		res := t4Login(h, tc.classCode, tc.passcode)
		if res.Status != http.StatusBadRequest {
			t.Errorf("空白字段用例 %d (%q,%q): 状态码 = %d, 期望 400; body=%v",
				i, tc.classCode, tc.passcode, res.Status, res.Body)
		}
	}

	// 非法 JSON 请求体 → 400
	if status, raw := t4RawRequest(h, http.MethodPost, "/api/teacher/login", "", "{bad json"); status != http.StatusBadRequest {
		t.Errorf("非法 JSON 登录状态码 = %d, 期望 400; body=%s", status, raw)
	}
}

// ---------- T3 角色双向隔离 ----------

// T3 角色隔离：学生 token 打 4 个教师端点一律 403+error；无/坏 token 401；
// 教师 token 打学生端点（points/pet/me/log）一律 403。
// 命令: go test ./server/ -run TestT4RoleIsolation -v
// 预期: 实现落地前 FAIL；落地后 PASS。
func TestT4RoleIsolation(t *testing.T) {
	h, dbPath := t4Handler(t)
	studentTok := mustJoin(t, h, "c301", "隔离生", "S001")
	teacherTok := t4TeacherToken(t, h, dbPath, "c301")

	teacherEndpoints := []struct {
		method string
		target string
		body   any
	}{
		{http.MethodPost, "/api/teacher/adopt", map[string]any{"studentNo": "S001"}},
		{http.MethodPost, "/api/teacher/points", map[string]any{"studentNo": "S001", "reason": "作业优秀", "value": 1}},
		{http.MethodGet, "/api/teacher/roster", nil},
		{http.MethodPost, "/api/teacher/passcode", map[string]any{"oldPasscode": "a", "newPasscode": "b"}},
	}

	// 学生 token 打教师端点 → 403 + error（PRD 硬要求）
	for _, ep := range teacherEndpoints {
		resp, body := doJSON(t, h, ep.method, ep.target, studentTok, ep.body)
		expectError(t, resp.StatusCode, body, http.StatusForbidden, "学生 token 打 "+ep.method+" "+ep.target)
	}

	// 无 token / 坏 token（学生 token 篡改 / 教师 token 篡改）→ 401 + error
	for _, tok := range []string{"", m2TamperToken(studentTok), m2TamperToken(teacherTok)} {
		for _, ep := range teacherEndpoints {
			resp, body := doJSON(t, h, ep.method, ep.target, tok, ep.body)
			expectError(t, resp.StatusCode, body, http.StatusUnauthorized,
				"坏 token 打 "+ep.method+" "+ep.target)
		}
	}

	// 教师 token 打学生端点 → 403（双向隔离）
	studentEndpoints := []struct {
		method string
		target string
		body   any
	}{
		{http.MethodPost, "/api/points", map[string]any{"reason": "作业优秀", "value": 1}},
		{http.MethodGet, "/api/pet/me", nil},
		{http.MethodGet, "/api/pet/me/log", nil},
	}
	for _, ep := range studentEndpoints {
		resp, body := doJSON(t, h, ep.method, ep.target, teacherTok, ep.body)
		expectError(t, resp.StatusCode, body, http.StatusForbidden, "教师 token 打 "+ep.method+" "+ep.target)
	}
}

// ---------- T8 方法限制 ----------

// T8 405：错误方法打教师端点一律 405（必须用方法级 pattern 显式注册；
// 否则 GET 会落入 "GET /" SPA 回退返回 200——本项目已知坑）。
// 命令: go test ./server/ -run TestT4MethodNotAllowed -v
// 预期: 实现落地前 FAIL（GET 类 404/200、POST 类 404）；落地后全部 405。
func TestT4MethodNotAllowed(t *testing.T) {
	h, dbPath := t4Handler(t)
	mustJoin(t, h, "c801", "方法班学生", "S001")
	teacherTok := t4TeacherToken(t, h, dbPath, "c801")

	// 带 token 的错误方法（authed 端点）
	cases := []struct{ method, target string }{
		{http.MethodGet, "/api/teacher/adopt"},
		{http.MethodGet, "/api/teacher/points"},
		{http.MethodPut, "/api/teacher/points"},
		{http.MethodGet, "/api/teacher/passcode"},
		{http.MethodPost, "/api/teacher/roster"},
		{http.MethodPut, "/api/teacher/roster"},
		{http.MethodDelete, "/api/teacher/roster"},
		{http.MethodPatch, "/api/teacher/roster"},
	}
	for _, tc := range cases {
		if code := m2Status(h, tc.method, tc.target, teacherTok, nil); code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s 状态码 = %d, 期望 405", tc.method, tc.target, code)
		}
	}

	// 登录口为 POST-only：无 token 的错误方法同样 405（登录本身不鉴权）
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if code := m2Status(h, m, "/api/teacher/login", "", nil); code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/teacher/login 状态码 = %d, 期望 405", m, code)
		}
	}
}
