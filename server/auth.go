package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// HMAC token：<b64url(studentID.issuedAt)>.<b64url(HMAC-SHA256)>。
// 密钥持久化在 meta 表（key=token_secret），服务重启后旧 token 仍有效。
const metaKeyTokenSecret = "token_secret"

func loadOrCreateTokenSecret(db *sql.DB) (string, error) {
	var secret string
	err := db.QueryRow(`SELECT v FROM meta WHERE k = ?`, metaKeyTokenSecret).Scan(&secret)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(buf)
	if _, err := db.Exec(`INSERT INTO meta(k, v) VALUES(?, ?)`, metaKeyTokenSecret, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func signToken(secret string, studentID int64) string {
	// 随机 nonce 保证每次 join 都签发不同 token（Windows 计时器粒度不可靠）
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	payload := strconv.FormatInt(studentID, 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." + base64.RawURLEncoding.EncodeToString(sig)
}

var errBadToken = errors.New("invalid token")

// verifyToken 校验签名并返回 studentID。
func verifyToken(secret, token string) (int64, error) {
	payloadB64, sigB64, ok := strings.Cut(token, ".")
	if !ok {
		return 0, errBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return 0, errBadToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return 0, errBadToken
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return 0, errBadToken
	}
	idStr, _, ok := strings.Cut(string(payload), ".")
	if !ok {
		return 0, errBadToken
	}
	studentID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || studentID <= 0 {
		return 0, errBadToken
	}
	return studentID, nil
}
