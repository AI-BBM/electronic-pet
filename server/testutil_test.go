package server_test

// M2 测试公共设施：黑盒风格，全部走真实 API（join/adopt/points）造数据，
// 复用 M1 的 helpers（newHandler/mustJoin/mustAdopt/performJSON 等），
// 命名统一加 m2 前缀避免与 M1 helper 冲突。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/AI-BBM/electronic-pet/server"
)

// m2PointsRequest 是 POST /api/points 的请求体形状。
type m2PointsRequest struct {
	Reason    string `json:"reason"`
	Value     int    `json:"value"`
	RequestID string `json:"requestId,omitempty"`
}

// m2LogItem 是流水单条记录的最小字段集。
type m2LogItem struct {
	ID        int64  `json:"id"`
	Value     int    `json:"value"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
}

// m2LogList 是 GET /api/pet/me/log 的响应体形状。
type m2LogList struct {
	Items    []m2LogItem `json:"items"`
	Page     int         `json:"page"`
	PageSize int         `json:"pageSize"`
	Total    int         `json:"total"`
}

// m2PointsResult 是一次加分请求的解码结果（仅测试 goroutine 使用）。
type m2PointsResult struct {
	Status  int
	Body    map[string]any
	Pet     pet
	LevelUp bool
	Level   int
	Added   bool
}

// m2AddPoints 调 POST /api/points 并解码响应（仅测试 goroutine 使用）。
func m2AddPoints(h http.Handler, token string, req m2PointsRequest) m2PointsResult {
	resp := performJSON(h, http.MethodPost, "/api/points", token, req)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return m2PointsResult{Status: resp.StatusCode}
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	res := m2PointsResult{Status: resp.StatusCode, Body: body}
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

// m2GetLog 调 GET /api/pet/me/log 并解码（query 形如 "?page=2" 或 ""，仅测试 goroutine 使用）。
func m2GetLog(h http.Handler, token, query string) (int, m2LogList, []byte) {
	resp := performJSON(h, http.MethodGet, "/api/pet/me/log"+query, token, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var list m2LogList
	_ = json.Unmarshal(raw, &list)
	return resp.StatusCode, list, raw
}

// m2JoinedToken join+adopt 后返回 token 与宠物（保证已有宠物，可加分）。
func m2JoinedToken(t *testing.T, h http.Handler) (string, pet) {
	t.Helper()
	token := mustJoin(t, h, "c101", "张小测", "S001")
	return token, mustAdopt(t, h, token, "random")
}

// m2SeedPoints 通过真实加分 API 把宠物累计到 total 分（value 全为 1），
// 每条请求断言 200 且 added=true（累计过程中跨级的 levelUp 不在此断言）。
func m2SeedPoints(t *testing.T, h http.Handler, token string, total int) {
	t.Helper()
	for i := 0; i < total; i++ {
		res := m2AddPoints(h, token, m2PointsRequest{
			Reason: "课堂表现", Value: 1, RequestID: fmt.Sprintf("seed-%s-%d", token[:8], i),
		})
		if res.Status != http.StatusOK || !res.Added {
			t.Fatalf("seed 第 %d 次加分失败: status=%d added=%v body=%v", i+1, res.Status, res.Added, res.Body)
		}
	}
}

// m2NewHandlerWithLevels 以自定义阈值 JSON 构建被测 handler（PET_LEVELS_FILE 生效）。
func m2NewHandlerWithLevels(t *testing.T, lv2, lv3 int) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "levels.json")
	content := fmt.Sprintf(`{"lv2":%d,"lv3":%d}`, lv2, lv3)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写阈值文件失败: %v", err)
	}
	t.Setenv(server.LevelsFileEnv, path)
	return newHandler(t)
}

// m2TamperToken 把合法 token 末位替换成另一个字符，制造篡改 token。
func m2TamperToken(token string) string {
	if token == "" {
		return "x"
	}
	last := token[len(token)-1]
	replacement := byte('A')
	if last == 'A' {
		replacement = 'B'
	}
	return token[:len(token)-1] + string(replacement)
}

// m2Status 发送任意方法/路径的请求，仅返回状态码（body 带 JSON 时自动携带）。
func m2Status(h http.Handler, method, target, token string, body any) int {
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	_ = body // 405 探测不关心请求体，统一 nil
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}
