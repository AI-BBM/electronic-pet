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

// 角色常量：学生 token payload "<studentID>.<nonce>"；教师 token payload
// "T<classID>.<nonce>"（M4 角色位）或 "T<classID>.<passVer>.<nonce>"（#53 密码
// 版本位，两段式视为 ver=0 继续有效）。学生 token 格式不变。
const (
	roleStudent = "student"
	roleTeacher = "teacher"
)

// principal 是 token 解析出的主体：学生（id=studentID）或教师（id=所属班级 id，
// ver=签发时的教师密码版本）。
type principal struct {
	role string
	id   int64
	ver  int64
}

func signToken(secret string, studentID int64) string {
	return signPrincipalToken(secret, principal{role: roleStudent, id: studentID})
}

// signTeacherToken 为班级签发教师 token（id=classID，ver=签发时教师密码版本）。
// 改密会递增 pass_ver，使携带旧版本的 token 全部失效（#53）。
func signTeacherToken(secret string, classID, ver int64) string {
	return signPrincipalToken(secret, principal{role: roleTeacher, id: classID, ver: ver})
}

func signPrincipalToken(secret string, p principal) string {
	// 随机 nonce 保证每次登录都签发不同 token（Windows 计时器粒度不可靠）
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	idPart := strconv.FormatInt(p.id, 10)
	if p.role == roleTeacher {
		idPart = "T" + idPart + "." + strconv.FormatInt(p.ver, 10)
	}
	payload := idPart + "." + base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." + base64.RawURLEncoding.EncodeToString(sig)
}

var (
	errBadToken  = errors.New("invalid token")
	errWrongRole = errors.New("wrong role")
)

// verifyToken 校验学生 token 并返回 studentID；教师 token 角色不符返回 errWrongRole。
func verifyToken(secret, token string) (int64, error) {
	p, err := verifyPrincipal(secret, token)
	if err != nil {
		return 0, err
	}
	if p.role != roleStudent {
		return 0, errWrongRole
	}
	return p.id, nil
}

// verifyTeacherToken 校验教师 token，返回 (classID, token 携带的密码版本)；
// 学生 token 角色不符返回 errWrongRole。
func verifyTeacherToken(secret, token string) (int64, int64, error) {
	p, err := verifyPrincipal(secret, token)
	if err != nil {
		return 0, 0, err
	}
	if p.role != roleTeacher {
		return 0, 0, errWrongRole
	}
	return p.id, p.ver, nil
}

func verifyPrincipal(secret, token string) (principal, error) {
	payloadB64, sigB64, ok := strings.Cut(token, ".")
	if !ok {
		return principal{}, errBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return principal{}, errBadToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return principal{}, errBadToken
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return principal{}, errBadToken
	}
	// 载荷按 "." 切分：学生 ["<id>", "<nonce>"]；教师 ["T<id>", "<ver>", "<nonce>"]
	// （三段式，#53）或 ["T<id>", "<nonce>"]（两段式旧格式，ver=0）。
	// 注意不能用「Cut 取首段再对首段二次 Cut」：三段式时首段只有 "T<id>"，
	// 二次 Cut 无分隔符会把 after 置空串导致 id 解析失败。
	segs := strings.Split(string(payload), ".")
	if len(segs) < 2 || len(segs) > 3 {
		return principal{}, errBadToken
	}
	idStr := segs[0]
	role := roleStudent
	ver := int64(0)
	if strings.HasPrefix(idStr, "T") {
		role = roleTeacher
		idStr = strings.TrimPrefix(idStr, "T")
		if len(segs) == 3 {
			v, err := strconv.ParseInt(segs[1], 10, 64)
			if err != nil || v < 0 {
				return principal{}, errBadToken
			}
			ver = v
		}
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return principal{}, errBadToken
	}
	return principal{role: role, id: id, ver: ver}, nil
}
