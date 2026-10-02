package server_test

// 对抗审查回灌用例（T17/T18）：
// - T17 抓「请求体无界解码」盲区：超大海量 body 必须被拒绝（413/400）且服务存活；
// - T18 抓「错误信息透传内部细节」盲区：所有非 2xx 的 error 字段不得含内部标记。

import (
	"net/http"
	"strings"
	"testing"
)

// TestJoinRejectsOversizedBody (T17) 1MB 的 name 必须被拒绝且服务存活。
func TestJoinRejectsOversizedBody(t *testing.T) {
	h := newHandler(t)

	huge := strings.Repeat("a", 1<<20) // 1MB
	res := joinStudent(h, "c1", huge, "01")
	if res.Status >= 500 {
		t.Fatalf("超大 body 应以 4xx 拒绝, 得到 %d", res.Status)
	}
	if res.Status != http.StatusRequestEntityTooLarge && res.Status != http.StatusBadRequest {
		t.Fatalf("超大 body 应返回 413 或 400, 得到 %d", res.Status)
	}

	// 服务必须仍然存活：随后正常 join 得到 200
	res2 := joinStudent(h, "c1", "小明", "01")
	if res2.Status != http.StatusOK || res2.Token == "" {
		t.Fatalf("拒绝超大 body 后正常 join 应 200, 得到 %d body=%v", res2.Status, res2.Body)
	}
}

// TestErrorMessagesDoNotLeakInternals (T18) 扫描各失败路径，error 不得泄漏内部标记。
func TestErrorMessagesDoNotLeakInternals(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c1", "小明", "01")
	ids := eggIDs(t, h, token)
	mustAdopt(t, h, token, ids[0])

	type probe struct {
		name   string
		status int
		body   map[string]any
	}
	var probes []probe

	// 401：无 token / 坏 token
	for _, method := range []struct{ m, p string }{
		{http.MethodGet, "/api/pet/me"},
		{http.MethodGet, "/api/eggs"},
	} {
		resp, body := doJSON(t, h, method.m, method.p, "", nil)
		probes = append(probes, probe{method.p + " 无token", resp.StatusCode, body})
		resp, body = doJSON(t, h, method.m, method.p, "not-a-valid-token", nil)
		probes = append(probes, probe{method.p + " 坏token", resp.StatusCode, body})
	}
	// 400：缺字段 / 非法 eggId
	resp, body := doJSON(t, h, http.MethodPost, "/api/join", "", map[string]string{})
	probes = append(probes, probe{"join 空字段", resp.StatusCode, body})
	resp, body = doJSON(t, h, http.MethodPost, "/api/adopt", token, map[string]string{"eggId": "egg-not-exist"})
	probes = append(probes, probe{"adopt 非法蛋", resp.StatusCode, body})
	// 409：重复 adopt / 重复改名
	resp, body = doJSON(t, h, http.MethodPost, "/api/adopt", token, map[string]string{"eggId": ids[0]})
	probes = append(probes, probe{"adopt 重复", resp.StatusCode, body})
	resp, body = doJSON(t, h, http.MethodPost, "/api/pet/name", token, map[string]string{"name": "新名字"})
	probes = append(probes, probe{"改名一次", resp.StatusCode, body})
	resp, body = doJSON(t, h, http.MethodPost, "/api/pet/name", token, map[string]string{"name": "再改"})
	probes = append(probes, probe{"改名二次", resp.StatusCode, body})
	// 413：超大 body（T17 路径的错误文案也在检查范围）
	resp, body = doJSON(t, h, http.MethodPost, "/api/join", "", map[string]string{"classCode": "c1", "name": strings.Repeat("a", 1<<20), "studentNo": "01"})
	probes = append(probes, probe{"join 超大body", resp.StatusCode, body})

	internalMarkers := []string{
		"sqlite", "constraint", "goroutine", "panic",
		"database is locked", "no such table", ".db", ":memory:",
	}
	for _, p := range probes {
		msg, ok := p.body["error"].(string)
		if !ok || msg == "" {
			continue // 部分 401 响应可能无 body 字段由其他用例覆盖
		}
		lower := strings.ToLower(msg)
		for _, marker := range internalMarkers {
			if strings.Contains(lower, marker) {
				t.Errorf("%s: error 字段泄漏内部信息 %q: %q", p.name, marker, msg)
			}
		}
		if strings.Contains(msg, "\\") {
			t.Errorf("%s: error 字段含路径分隔符（疑似泄漏路径）: %q", p.name, msg)
		}
	}
}
