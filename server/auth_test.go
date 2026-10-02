package server_test

// M2 鉴权黑盒用例（T6）：M2 两个端点均要求有效 Bearer token（M1 的 HMAC token 方案）。
// 命令: go test ./server/ -run TestAuth_Required -v

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuth_Required(t *testing.T) {
	h := newHandler(t)
	token, _ := m2JoinedToken(t, h)

	cases := []struct {
		name   string
		method string
		target string
		token  string
	}{
		{"POST /api/points 无 token", http.MethodPost, "/api/points", ""},
		{"POST /api/points 坏 token", http.MethodPost, "/api/points", "not-a-token"},
		{"POST /api/points 篡改 token", http.MethodPost, "/api/points", m2TamperToken(token)},
		{"GET /api/pet/me/log 无 token", http.MethodGet, "/api/pet/me/log", ""},
		{"GET /api/pet/me/log 坏 token", http.MethodGet, "/api/pet/me/log", strings.Repeat("x", 32)},
		{"GET /api/pet/me/log 篡改 token", http.MethodGet, "/api/pet/me/log", m2TamperToken(token)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			if tc.method == http.MethodPost {
				got = m2AddPoints(h, tc.token, m2PointsRequest{Reason: "作业优秀", Value: 1, RequestID: "t6"}).Status
			} else {
				status, _, _ := m2GetLog(h, tc.token, "")
				got = status
			}
			if got != http.StatusUnauthorized {
				t.Errorf("状态码 = %d, 期望 401", got)
			}
		})
	}

	// 合法 token 正常通过
	if res := m2AddPoints(h, token, m2PointsRequest{Reason: "作业优秀", Value: 1, RequestID: "t6-ok"}); res.Status != http.StatusOK || !res.Added {
		t.Errorf("合法 token 加分 = (status=%d added=%v), 期望 (200 true)", res.Status, res.Added)
	}
}
