package server_test

// 黑盒测试公共设施：只通过 server.New(dbPath) 构建被测 handler，
// 用 httptest 直接调用 ServeHTTP，不感知任何内部实现。
// 并发用例只使用不带 *testing.T 的 helper（joinStudent / adoptEgg / performJSON），
// 断言一律在测试 goroutine 内完成。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/AI-BBM/electronic-pet/server"
)

// petSpecies 是 M1 契约中 species 对象的最小字段集。
type petSpecies struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Rarity   string `json:"rarity"`
	ImageURL string `json:"imageUrl"`
}

// pet 是 M1 契约中 pet 对象的最小字段集。
type pet struct {
	Name            string     `json:"name"`
	Species         petSpecies `json:"species"`
	Level           int        `json:"level"`
	Points          int        `json:"points"`
	NextLevelPoints *int       `json:"nextLevelPoints"`
}

// isValidRarity 校验 rarity 取值集合。
func isValidRarity(r string) bool {
	return r == "common" || r == "rare" || r == "epic"
}

// newHandler 在独立临时目录的 SQLite 上构建被测 handler，保证测间隔离。
func newHandler(t *testing.T) http.Handler {
	t.Helper()
	return newHandlerAt(t, filepath.Join(t.TempDir(), "pet.db"))
}

// newHandlerAt 在指定 dbPath 上构建被测 handler（重启场景复用同一 DB 文件）。
func newHandlerAt(t *testing.T, dbPath string) http.Handler {
	t.Helper()
	h, err := server.New(dbPath)
	if err != nil {
		t.Fatalf("server.New(%q) 返回错误: %v", dbPath, err)
	}
	return h
}

// performJSON 发送 JSON 请求并返回录制响应；不接触 testing.T，可在 goroutine 中使用。
func performJSON(h http.Handler, method, target, token string, body any) *http.Response {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			panic(fmt.Sprintf("marshal 请求体失败: %v", err))
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// doJSON 发送 JSON 请求并把响应体解码为 JSON 对象（仅限测试 goroutine 使用）。
func doJSON(t *testing.T, h http.Handler, method, target, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	resp := performJSON(h, method, target, token, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("响应不是 JSON 对象: %s", raw)
	}
	return resp, decoded
}

// joinResult 是 /api/join 的结果快照。
type joinResult struct {
	Status int
	Token  string
	Body   map[string]any
	Err    error
}

// joinStudent 调用 /api/join；可在 goroutine 中使用。
func joinStudent(h http.Handler, classCode, name, studentNo string) joinResult {
	resp := performJSON(h, http.MethodPost, "/api/join", "", map[string]string{
		"classCode": classCode,
		"name":      name,
		"studentNo": studentNo,
	})
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return joinResult{Err: err}
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return joinResult{Status: resp.StatusCode, Err: fmt.Errorf("join 响应不是 JSON: %s", raw)}
	}
	token, _ := body["token"].(string)
	return joinResult{Status: resp.StatusCode, Token: token, Body: body}
}

// adoptEgg 调用 /api/adopt；可在 goroutine 中使用。
func adoptEgg(h http.Handler, token, eggID string) (int, []byte) {
	resp := performJSON(h, http.MethodPost, "/api/adopt", token, map[string]string{"eggId": eggID})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// mustJoin 断言 join 成功并返回 token。
func mustJoin(t *testing.T, h http.Handler, classCode, name, studentNo string) string {
	t.Helper()
	res := joinStudent(h, classCode, name, studentNo)
	if res.Err != nil {
		t.Fatalf("join(%s/%s/%s) 失败: %v", classCode, name, studentNo, res.Err)
	}
	if res.Status != http.StatusOK {
		t.Fatalf("join(%s/%s/%s) 状态码 = %d, 期望 200, body=%v", classCode, name, studentNo, res.Status, res.Body)
	}
	if res.Token == "" {
		t.Fatalf("join(%s/%s/%s) 未返回 token", classCode, name, studentNo)
	}
	return res.Token
}

// mustAdopt 断言 adopt 成功并返回解码后的 pet。
func mustAdopt(t *testing.T, h http.Handler, token, eggID string) pet {
	t.Helper()
	status, raw := adoptEgg(h, token, eggID)
	if status != http.StatusOK {
		t.Fatalf("adopt(%q) 状态码 = %d, 期望 200, body=%s", eggID, status, raw)
	}
	return petFromEnvelope(t, raw)
}

// petFromEnvelope 从 {"pet":{...}} 形态的响应体解码 pet。
func petFromEnvelope(t *testing.T, raw []byte) pet {
	t.Helper()
	var env struct {
		Pet pet `json:"pet"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("pet 信封解码失败 (%s): %v", raw, err)
	}
	return env.Pet
}

// petFromAny 把已解码为 map 的 pet 对象再解码为类型化 pet。
func petFromAny(t *testing.T, v any) pet {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("pet 不是 JSON 对象: %v", v)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("重新编码 pet 失败: %v", err)
	}
	var p pet
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("pet 解码失败 (%s): %v", raw, err)
	}
	return p
}

// checkPetShape 校验 M1 契约的 pet 最小字段集（不含等级/分数字段断言）。
func checkPetShape(t *testing.T, p pet) {
	t.Helper()
	if p.Name == "" {
		t.Errorf("pet.name 为空")
	}
	if p.Species.ID == "" {
		t.Errorf("pet.species.id 为空")
	}
	if p.Species.Name == "" {
		t.Errorf("pet.species.name 为空")
	}
	if !isValidRarity(p.Species.Rarity) {
		t.Errorf("pet.species.rarity = %q, 期望 common/rare/epic 之一", p.Species.Rarity)
	}
	if p.Species.ImageURL == "" {
		t.Errorf("pet.species.imageUrl 为空")
	}
}

// expectError 断言错误响应：状态码精确匹配且携带非空 {"error":"..."}。
func expectError(t *testing.T, gotStatus int, body map[string]any, wantStatus int, context string) {
	t.Helper()
	if gotStatus != wantStatus {
		t.Errorf("%s: 状态码 = %d, 期望 %d", context, gotStatus, wantStatus)
	}
	msg, ok := body["error"].(string)
	if !ok || msg == "" {
		t.Errorf("%s: 响应缺少非空 error 字段: %v", context, body)
	}
}

// eggIDs 返回 /api/eggs 的全部蛋 id。
func eggIDs(t *testing.T, h http.Handler, token string) []string {
	t.Helper()
	resp, body := doJSON(t, h, http.MethodGet, "/api/eggs", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/eggs 状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
	}
	eggs, ok := body["eggs"].([]any)
	if !ok {
		t.Fatalf("/api/eggs 响应缺少 eggs 数组: %v", body)
	}
	ids := make([]string, 0, len(eggs))
	for i, raw := range eggs {
		egg, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("第 %d 颗蛋不是 JSON 对象: %v", i, raw)
		}
		id, _ := egg["id"].(string)
		if id == "" {
			t.Fatalf("第 %d 颗蛋 id 为空", i)
		}
		ids = append(ids, id)
	}
	return ids
}

// invalidEggID 构造一个确定不在蛋列表中的 eggId。
func invalidEggID(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	ids := eggIDs(t, h, token)
	exists := make(map[string]bool, len(ids))
	for _, id := range ids {
		exists[id] = true
	}
	for _, cand := range []string{"egg-not-exist", "egg-999", "not-an-egg"} {
		if !exists[cand] {
			return cand
		}
	}
	t.Fatal("无法构造非法 eggId：候选值全部命中蛋列表")
	return ""
}
