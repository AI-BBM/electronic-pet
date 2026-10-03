package server_test

// Issue #21 回归：我的宠物页加分按钮不得再有「M2 未上线」灰显与过期文案，
// 且必须接入 /api/points 的加分表单（预设理由 + 分值 + 前端进化动画组件）。
// 命令: go test ./server/ -run TestPointsButtonWired_NoStaleGate -v

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPointsButtonWired_NoStaleGate(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / 状态码 = %d, 期望 200", rec.Code)
	}
	body := rec.Body.String()

	for _, stale := range []string{"M2 即将开放", "将在 M2 上线"} {
		if strings.Contains(body, stale) {
			t.Errorf("首页仍存在过期文案 %q", stale)
		}
	}

	start := strings.Index(body, `id="add-points-btn"`)
	if start < 0 {
		t.Fatal("首页缺少加分按钮 add-points-btn")
	}
	tagStart := strings.LastIndex(body[:start], "<button")
	tagEnd := strings.Index(body[start:], ">")
	if tagStart < 0 || tagEnd < 0 {
		t.Fatal("无法解析加分按钮标签")
	}
	tag := body[tagStart : start+tagEnd+1]
	if strings.Contains(tag, "disabled") {
		t.Errorf("加分按钮仍被禁用: %s", tag)
	}

	for _, marker := range []string{
		`id="points-form"`,
		`id="points-reason"`,
		`>作业优秀<`,
		`id="points-value"`,
		`src="/js/points.js"`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("首页缺少加分接线要素 %s", marker)
		}
	}
}
