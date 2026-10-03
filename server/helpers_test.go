package server_test

// 黑盒测试公共设施（M6 处置后精简版）：只通过 server.New(dbPath) 构建被测
// handler，用 httptest 直接调用 ServeHTTP。学生端 helper（join/adopt/eggs 等）
// 已随 M6 学生侧下线处置删除，仅保留构建器与通用断言。

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

// newHandler 在独立临时目录的 SQLite 上构建被测 handler，保证测间隔离。
func newHandler(t *testing.T) http.Handler {
	t.Helper()
	return newHandlerAt(t, filepath.Join(t.TempDir(), "pet.db"))
}

// newHandlerAt 在指定 dbPath 上构建被测 handler（重启/迁移场景复用同一 DB 文件）。
// 测试结束自动 Close 释放 SQLite 句柄（Windows 下文件被占用会导致 TempDir 清理失败），
// 多次调用时按 t.Cleanup LIFO 逆序关闭。
func newHandlerAt(t *testing.T, dbPath string) http.Handler {
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
