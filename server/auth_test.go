package server

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// T12 SignToken/VerifyToken 往返：格式为 "<id>.<hex>"，HMAC-SHA256 摘要 32 字节，能取回学生 id。
// 命令: go test ./server/ -run TestToken_SignAndVerify_RoundTrip -v
func TestToken_SignAndVerify_RoundTrip(t *testing.T) {
	secret := []byte("unit-secret")
	for _, id := range []int64{1, 42, 123456789012} {
		token := SignToken(secret, id)
		parts := strings.SplitN(token, ".", 2)
		if len(parts) != 2 {
			t.Fatalf("id=%d: token %q 不符合 \"<id>.<hex>\" 格式", id, token)
		}
		if want := fmt.Sprintf("%d", id); parts[0] != want {
			t.Errorf("id=%d: token 前缀 = %q, 期望 %q", id, parts[0], want)
		}
		mac, err := hex.DecodeString(parts[1])
		if err != nil {
			t.Fatalf("id=%d: token 摘要部分 %q 不是合法 hex: %v", id, parts[1], err)
		}
		if len(mac) != 32 {
			t.Errorf("id=%d: HMAC-SHA256 摘要长度 = %d 字节, 期望 32", id, len(mac))
		}
		gotID, err := VerifyToken(secret, token)
		if err != nil {
			t.Fatalf("id=%d: VerifyToken 报错: %v", id, err)
		}
		if gotID != id {
			t.Errorf("VerifyToken 返回 id = %d, 期望 %d", gotID, id)
		}
	}
}

// T12 篡改的 token 必须校验失败。
// 命令: go test ./server/ -run TestToken_Verify_RejectsTampered -v
func TestToken_Verify_RejectsTampered(t *testing.T) {
	secret := []byte("unit-secret")
	token := SignToken(secret, 7)
	parts := strings.SplitN(token, ".", 2)

	// 确定性地把摘要末字符换成另一个字符
	last := parts[1][len(parts[1])-1]
	repl := byte('a')
	if last == 'a' {
		repl = 'b'
	}
	tamperedMAC := token[:len(token)-1] + string(repl)

	badTokens := map[string]string{
		"篡改摘要末字符":   tamperedMAC,
		"篡改学生id":    "8." + parts[1],
		"非token纯文本": "not-a-token",
		"空字符串":      "",
		"无点分隔符":     "abc",
		"摘要非hex":    "7.zzzz",
	}
	for name, tok := range badTokens {
		if _, err := VerifyToken(secret, tok); err == nil {
			t.Errorf("%s: VerifyToken(%q) 应当失败, 却返回 nil error", name, tok)
		}
	}
}

// T12 用错误 secret 校验合法 token 必须失败（双向）。
// 命令: go test ./server/ -run TestToken_Verify_RejectsWrongSecret -v
func TestToken_Verify_RejectsWrongSecret(t *testing.T) {
	tokenA := SignToken([]byte("secret-a"), 5)
	if _, err := VerifyToken([]byte("secret-b"), tokenA); err == nil {
		t.Error("用 secret-b 校验 secret-a 签发的 token 应当失败")
	}
	tokenB := SignToken([]byte("secret-b"), 5)
	if _, err := VerifyToken([]byte("secret-a"), tokenB); err == nil {
		t.Error("用 secret-a 校验 secret-b 签发的 token 应当失败")
	}
}

// T6 鉴权：缺失 Authorization、Bearer 坏 token、跨 secret token → 401；合法 token → 200。
// 命令: go test ./server/ -run TestAuth_Required -v
func TestAuth_Required(t *testing.T) {
	env := newTestEnv(t)
	studentID, _ := seedStudentWithPet(t, env.store.DB(), 1, 0)
	validBody := pointsPostReq{Reason: "作业优秀", Value: 1, RequestID: "t6-1"}

	// 无 Authorization 头 → 401（POST 与 GET 均要求鉴权）
	if status, _ := env.do(t, http.MethodPost, "/api/points", "", validBody); status != http.StatusUnauthorized {
		t.Errorf("无 Authorization 头 POST /api/points 状态码 = %d, 期望 401", status)
	}
	if status, _ := env.do(t, http.MethodGet, "/api/pet/me/log", "", nil); status != http.StatusUnauthorized {
		t.Errorf("无 Authorization 头 GET /api/pet/me/log 状态码 = %d, 期望 401", status)
	}

	// Bearer 坏 token → 401
	if status, _ := env.do(t, http.MethodPost, "/api/points", "definitely-not-a-valid-token", validBody); status != http.StatusUnauthorized {
		t.Errorf("坏 token POST /api/points 状态码 = %d, 期望 401", status)
	}

	// 其他 secret 签发的合法形态 token → 401
	foreign := SignToken([]byte("another-secret"), studentID)
	if status, _ := env.do(t, http.MethodPost, "/api/points", foreign, validBody); status != http.StatusUnauthorized {
		t.Errorf("跨 secret token POST /api/points 状态码 = %d, 期望 401", status)
	}

	// 合法 token → 200 且正常计分
	status, body := env.do(t, http.MethodPost, "/api/points", env.token(studentID), validBody)
	if status != http.StatusOK {
		t.Fatalf("合法 token POST /api/points 状态码 = %d, 期望 200; body=%s", status, body)
	}
	if resp := decodePointsResp(t, body); !resp.Added {
		t.Errorf("合法 token 加分 added = false, 期望 true; body=%s", body)
	}
}
