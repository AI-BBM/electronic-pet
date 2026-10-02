package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// testSecret 是本包 HTTP 用例共用的 token 签名密钥。
const testSecret = "tdd-secret-do-not-use-in-prod"

// testEnv 聚合一次用例所需的存储与被测 HTTP 服务（临时 SQLite 文件，用例间完全隔离）。
type testEnv struct {
	store  *Store
	secret []byte
	srv    *httptest.Server
}

// newTestEnvWithLevels 以指定等级阈值启动一套隔离的测试环境。
func newTestEnvWithLevels(t *testing.T, levels LevelConfig) *testEnv {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "pet.db"))
	if err != nil {
		t.Fatalf("OpenStore 失败: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭 Store 失败: %v", err)
		}
	})
	srv := httptest.NewServer(NewMux(store, levels, []byte(testSecret)))
	t.Cleanup(srv.Close)
	return &testEnv{store: store, secret: []byte(testSecret), srv: srv}
}

// newTestEnv 以默认等级阈值（Lv2=20, Lv3=60）启动测试环境。
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWithLevels(t, DefaultLevels())
}

// token 为指定学生签发合法 Bearer token。
func (e *testEnv) token(studentID int64) string {
	return SignToken(e.secret, studentID)
}

// do 发送一次 HTTP 请求：body 非 nil 时按 JSON 序列化携带；token 为空表示不带 Authorization 头。
// 返回响应状态码与响应体原文。注意：内部只使用 t.Errorf，可在 goroutine 中安全调用。
func (e *testEnv) do(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Errorf("构造 %s %s 请求体失败: %v", method, path, err)
			return 0, nil
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		t.Errorf("构造 %s %s 请求失败: %v", method, path, err)
		return 0, nil
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Errorf("%s %s 请求失败: %v", method, path, err)
		return 0, nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("读取 %s %s 响应体失败: %v", method, path, err)
		return resp.StatusCode, nil
	}
	return resp.StatusCode, data
}

// seedClass 插入一个班级并返回其 id。
func seedClass(t *testing.T, db *sql.DB, code string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO classes (code) VALUES (?)`, code)
	if err != nil {
		t.Fatalf("seed 班级失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("取班级 id 失败: %v", err)
	}
	return id
}

// seedStudent 插入一名学生并返回其 id。
func seedStudent(t *testing.T, db *sql.DB, classID int64, name, studentNo string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO students (class_id, name, student_no) VALUES (?, ?, ?)`,
		classID, name, studentNo,
	)
	if err != nil {
		t.Fatalf("seed 学生失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("取学生 id 失败: %v", err)
	}
	return id
}

// seedPet 为学生插入一只宠物（指定等级与积分）并返回宠物 id。
func seedPet(t *testing.T, db *sql.DB, studentID int64, level, points int) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO pets (student_id, species, rarity, name, level, points, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		studentID, "robotcat", "N", "测试宠物", level, points, "2026-01-01 09:00:00",
	)
	if err != nil {
		t.Fatalf("seed 宠物失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("取宠物 id 失败: %v", err)
	}
	return id
}

// seedStudentWithPet 一键创建「班级 + 学生 + 宠物」，返回学生 id 与宠物 id。
func seedStudentWithPet(t *testing.T, db *sql.DB, level, points int) (studentID, petID int64) {
	t.Helper()
	classID := seedClass(t, db, "C101")
	studentID = seedStudent(t, db, classID, "张小测", "S001")
	petID = seedPet(t, db, studentID, level, points)
	return studentID, petID
}

// seedLogs 绕过 API 直接为宠物灌入 n 条流水（id 自增 1..n），值在 1..10 内循环。
func seedLogs(t *testing.T, db *sql.DB, petID int64, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		_, err := db.Exec(
			`INSERT INTO point_logs (pet_id, value, reason, request_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			petID, (i-1)%10+1, fmt.Sprintf("课堂加分%03d", i), fmt.Sprintf("seed-%d-%d", petID, i),
			fmt.Sprintf("2026-01-02 15:04:%02d", i%60),
		)
		if err != nil {
			t.Fatalf("seed 流水第 %d 条失败: %v", i, err)
		}
	}
}

// petState 读取宠物的 (level, points)，用于校验「档案与流水立即一致」。
func petState(t *testing.T, db *sql.DB, petID int64) (level, points int) {
	t.Helper()
	if err := db.QueryRow(`SELECT level, points FROM pets WHERE id = ?`, petID).Scan(&level, &points); err != nil {
		t.Fatalf("读取宠物状态失败: %v", err)
	}
	return level, points
}

// logCount 统计指定宠物的流水条数。
func logCount(t *testing.T, db *sql.DB, petID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM point_logs WHERE pet_id = ?`, petID).Scan(&n); err != nil {
		t.Fatalf("统计流水失败: %v", err)
	}
	return n
}

// decodePointsResp 解析 POST /api/points 的响应体（错误只记不中断，可在 goroutine 中调用）。
func decodePointsResp(t *testing.T, body []byte) pointsPostResp {
	t.Helper()
	var resp pointsPostResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Errorf("响应不是合法的加分响应 JSON: %v, body=%s", err, body)
	}
	return resp
}

// decodeLogList 解析 GET /api/pet/me/log 的响应体。
func decodeLogList(t *testing.T, body []byte) logListJSON {
	t.Helper()
	var resp logListJSON
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Errorf("响应不是合法的流水响应 JSON: %v, body=%s", err, body)
	}
	return resp
}

// hasErrorField 判断 JSON 响应体是否包含非空 error 字段。
func hasErrorField(t *testing.T, body []byte) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Errorf("响应体不是 JSON 对象: %v, body=%s", err, body)
		return false
	}
	v, ok := m["error"]
	return ok && v != nil
}

// rawPetField 从响应原文中提取 pet.<field> 的原始 JSON 文本，用于严格区分 null 与字段缺失。
func rawPetField(t *testing.T, body []byte, field string) (string, bool) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Errorf("响应体不是 JSON 对象: %v, body=%s", err, body)
		return "", false
	}
	petRaw, ok := top["pet"]
	if !ok {
		return "", false
	}
	var pet map[string]json.RawMessage
	if err := json.Unmarshal(petRaw, &pet); err != nil {
		t.Errorf("响应 pet 字段不是 JSON 对象: %v, body=%s", err, body)
		return "", false
	}
	v, ok := pet[field]
	if !ok {
		return "", false
	}
	return string(v), true
}
