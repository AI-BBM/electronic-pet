package server_test

// #53 教师自助改密（P1 登录后改密 / P3 旧 token 全量失效）测试契约（TDD 红灯先行）。
//
// 黑盒口径与既有里程碑一致：仅经 server.New(dbPath) 构建的 handler 发真实 HTTP；
// 例外是任务书允许的直查 DB（断言 teachers.pass_ver）与「旧两段式 token 直签」
// —— 签名格式由 auth.go 公开契约钉死（payload "T<classID>.<nonce>" 两段 base64url
// + HMAC-SHA256，密钥在 meta 表 key=token_secret），复用既有 m6TeacherTokenForClass。
//
// 依赖说明：
//   - 复用 helpers_test.go / m6_helpers_test.go 的黑盒核心（m6Handler/m6Do/m6Status/
//     expectError/performJSON/m6BadToken/m6RegisterTeacher/m6Login/m6Roster 等），
//     本 Issue 新符号一律 m53 前缀。
//   - 注册链路依赖「未配置 SMTP_HOST → mock 发信」：跑本测试的环境不得设置
//     SMTP_HOST 环境变量（go test 进程默认继承宿主环境）。
//
// 实现前红灯分层（预期全部为运行时行为断言失败，无编译错误）：
//   - POST /api/teacher/change-password 未注册 → 落入 /api/ 兜底 404；
//   - teachers 表无 pass_ver 列 → m53PassVer 直查报 no such column；
//   - docs/ops/teacher-password-reset.md 不存在 → T1 失败。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------- 常量与 helpers（m53 前缀） ----------

const (
	// m53Path 是被测端点路径。
	m53Path = "/api/teacher/change-password"
	// m53NewPass 是契约示例新密码（≥8 字节）。
	m53NewPass = "newpass123"
	// m53StaleTokenMsg 钉死改密后旧 token 的 401 文案（P3 实现口径）。
	m53StaleTokenMsg = "登录已过期，请重新登录"
)

// m53Register 注册一名全新教师并返回其登录态 token（注册即签发 token）。
func m53Register(t *testing.T, h http.Handler, dbPath, email string) string {
	t.Helper()
	return m6RegisterTeacher(t, h, dbPath, email, "五三班")
}

// m53Change 调 POST /api/teacher/change-password（JSON 字段名按任务书钉死）。
func m53Change(h http.Handler, token, oldPassword, newPassword string) (int, map[string]any) {
	status, body, _ := m6Do(h, http.MethodPost, m53Path, token, map[string]string{
		"oldPassword": oldPassword,
		"newPassword": newPassword,
	})
	return status, body
}

// m53ChangeRaw 以原始字符串请求体调改密端点（坏 JSON 用例——performJSON 只能发
// 合法 JSON，坏 JSON 必须绕开 marshal 直接写字节）。
func m53ChangeRaw(h http.Handler, token, rawBody string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, m53Path, strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded
}

// m53PassVer 直查 teachers.pass_ver（#53 P3：改密成功一次自增 1，使旧 token 失效；
// 迁移默认 0）。列缺失（实现未落地）时 Fatal——这正是红灯之一。
func m53PassVer(t *testing.T, dbPath, email string) int64 {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	var ver int64
	if err := db.QueryRow(`SELECT pass_ver FROM teachers WHERE email = ?`, email).Scan(&ver); err != nil {
		t.Fatalf("查询 %s 的 pass_ver 失败（teachers 表必须迁移出 pass_ver 列）: %v", email, err)
	}
	return ver
}

// m53ClassIDByEmail 按邮箱查教师所属班级 id（直查 DB，黑盒接口不暴露 class_id）。
func m53ClassIDByEmail(t *testing.T, dbPath, email string) int64 {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	var id int64
	if err := db.QueryRow(`SELECT class_id FROM teachers WHERE email = ?`, email).Scan(&id); err != nil {
		t.Fatalf("查询 %s 的 class_id 失败: %v", email, err)
	}
	return id
}

// m53LegacyToken 为教师签发「旧两段式」token：直接复用既有 m6TeacherTokenForClass
// ——它按 auth.go 公开契约签 payload "T<classID>.<nonce>"（两段式），即 P3 升级前
// 的旧格式。契约：改密前 ver=0 == pass_ver=0 恒有效（向后兼容）；改密后
// pass_ver≥1 即失效。这是本文件唯一一处密码学构造（经由既有 helper 完成）。
func m53LegacyToken(t *testing.T, dbPath, email string) string {
	t.Helper()
	return m6TeacherTokenForClass(t, dbPath, m53ClassIDByEmail(t, dbPath, email))
}

// m53SignStudentToken 按 auth.go 公开契约签「学生式」token：payload
// "<studentID>.<nonce>"（无 T 前缀）+ HMAC-SHA256，两段 base64url。
// 用途：requireTeacher 对「签名合法但角色不符」的 token 返回 403（errWrongRole），
// 因此 HMAC 必须真算——坏签名走不到角色分支（401）。studentID 取任意正数即可
// （角色判定发生在任何查库之前）。这是黑盒外第二处密码学构造，依据即 auth.go
// 的公开 token 格式。
func m53SignStudentToken(secret string, studentID int64) string {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	payload := fmt.Sprintf("%d.%s", studentID, base64.RawURLEncoding.EncodeToString(nonce))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// m53StudentToken 读 meta.token_secret（键名与 auth.go metaKeyTokenSecret 一致）
// 并签发学生式 token（打教师端点应 403）。
func m53StudentToken(t *testing.T, dbPath string) string {
	t.Helper()
	db := m6OpenDB(t, dbPath)
	var secret string
	if err := db.QueryRow(`SELECT v FROM meta WHERE k = 'token_secret'`).Scan(&secret); err != nil {
		t.Fatalf("读取 token_secret 失败: %v", err)
	}
	return m53SignStudentToken(secret, 1)
}

// m53MustLogin 断言登录成功并返回新 token。
func m53MustLogin(t *testing.T, h http.Handler, email, password, context string) string {
	t.Helper()
	status, body := m6Login(h, email, password)
	if status != http.StatusOK {
		t.Fatalf("%s: login(%s) 状态码 = %d, 期望 200; body=%v", context, email, status, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("%s: login(%s) 未返回 token: %v", context, email, body)
	}
	return token
}

// m53ExpectStale 断言「改密后旧 token」访问教师端点：401 + 钉死的过期文案
// （P3 实现口径：ver 不匹配 → 401 {"error":"登录已过期，请重新登录"}）。
func m53ExpectStale(t *testing.T, h http.Handler, token, context string) {
	t.Helper()
	status, body, _ := m6Do(h, http.MethodGet, "/api/teacher/roster", token, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("%s: 调 roster 状态码 = %d, 期望 401; body=%v", context, status, body)
		return
	}
	if msg, _ := body["error"].(string); msg != m53StaleTokenMsg {
		t.Errorf("%s: error = %q, 期望精确 %q", context, msg, m53StaleTokenMsg)
	}
}

// ---------- T1 P2 文档结构 ----------

// T1 管理员重置 SOP 文档存在（P2 交付物；仓库根相对本测试文件为 ../docs/ops/）。
// 命令: go test ./server/ -run TestM53ResetSOPDocExists -v
func TestM53ResetSOPDocExists(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 无法定位测试文件所在目录")
	}
	root := filepath.Dir(filepath.Dir(thisFile)) // server/ 的上一级即仓库根
	if _, err := os.Stat(filepath.Join(root, "docs", "ops", "teacher-password-reset.md")); err != nil {
		t.Errorf("缺少管理员重置 SOP 文档 docs/ops/teacher-password-reset.md: %v", err)
	}
}

// ---------- T2 成功改密 + 所有旧 token 失效 ----------

// T2 端到端主链路：注册/登录/旧两段式三种 token 改密前均有效 → 改密成功
// 200 {"ok":true}、DB pass_ver=1 → 三种旧 token 全部 401（精确过期文案）→
// 旧密码 login 401、新密码 login 200 且新 token 调 roster 200。
// 命令: go test ./server/ -run TestM53ChangePasswordSuccessInvalidatesOldTokens -v
func TestM53ChangePasswordSuccessInvalidatesOldTokens(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "m53-ok@example.com"
	tokRegister := m53Register(t, h, dbPath, email)          // 会话1：注册签发
	tokLogin := m53MustLogin(t, h, email, m6Password, "改密前") // 会话2：登录签发
	tokLegacy := m53LegacyToken(t, dbPath, email)            // 会话3：旧两段式直签

	// 改密前：三种 token 均可访问教师端点（旧两段式 = ver 0，向后兼容钉死）
	for _, tc := range []struct{ name, token string }{
		{"注册token", tokRegister},
		{"登录token", tokLogin},
		{"旧两段式token", tokLegacy},
	} {
		if status, _, body := m6Roster(h, tc.token); status != http.StatusOK {
			t.Fatalf("改密前 %s 调 roster 状态码 = %d, 期望 200; body=%v", tc.name, status, body)
		}
	}

	status, body := m53Change(h, tokRegister, m6Password, m53NewPass)
	if status != http.StatusOK {
		t.Fatalf("改密状态码 = %d, 期望 200; body=%v", status, body)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Errorf("改密响应 = %v, 期望 {\"ok\":true}", body)
	}
	if ver := m53PassVer(t, dbPath, email); ver != 1 {
		t.Errorf("改密后 pass_ver = %d, 期望 1", ver)
	}

	// P3：所有旧 token（注册/登录/旧两段式）一律失效
	m53ExpectStale(t, h, tokRegister, "注册token(改密后)")
	m53ExpectStale(t, h, tokLogin, "登录token(改密后)")
	m53ExpectStale(t, h, tokLegacy, "旧两段式token(改密后)")

	// 旧密码 401；新密码 200 且新 token 可用
	if status, body := m6Login(h, email, m6Password); status != http.StatusUnauthorized {
		t.Errorf("旧密码 login 状态码 = %d, 期望 401; body=%v", status, body)
	}
	tokNew := m53MustLogin(t, h, email, m53NewPass, "改密后")
	if status, _, body := m6Roster(h, tokNew); status != http.StatusOK {
		t.Errorf("新 token 调 roster 状态码 = %d, 期望 200; body=%v", status, body)
	}
}

// ---------- T3 旧密码错误 ----------

// T3 旧密码错 → 403 + error；且无任何副作用：pass_ver 仍 0、原密码仍可登录、
// 新密码不得登录成功。
// 命令: go test ./server/ -run TestM53ChangePasswordWrongOld -v
func TestM53ChangePasswordWrongOld(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "m53-wrongold@example.com"
	token := m53Register(t, h, dbPath, email)

	status, body := m53Change(h, token, "wrong-old-99", m53NewPass)
	expectError(t, status, body, http.StatusForbidden, "旧密码错误")

	if ver := m53PassVer(t, dbPath, email); ver != 0 {
		t.Errorf("旧密码错误后 pass_ver = %d, 期望 0（密码未变）", ver)
	}
	if status, _ := m6Login(h, email, m6Password); status != http.StatusOK {
		t.Errorf("原密码 login 状态码 = %d, 期望 200（密码未变）", status)
	}
	if status, _ := m6Login(h, email, m53NewPass); status != http.StatusUnauthorized {
		t.Errorf("新密码 login 状态码 = %d, 期望 401（密码未变）", status)
	}
}

// ---------- T4 新密码长度边界（按字节 8..72，独立账号） ----------

// T4 长度边界：7 字节/73 字节 → 400；8 字节/72 字节 → 200；另钉「按字节而非
// rune」语义——9 字节 3 rune 的多字节密码合法（bcrypt 上限按字节计，≥8 字节即合规）。
// 每个用例独立教师账号、独立 DB；400 后密码未变，200 后 pass_ver=1 且新密码可登录。
// 命令: go test ./server/ -run TestM53ChangePasswordLengthBounds -v
func TestM53ChangePasswordLengthBounds(t *testing.T) {
	cases := []struct {
		name string
		pw   string
		want int
	}{
		{"7字节", "1234567", http.StatusBadRequest},
		{"73字节", strings.Repeat("a", 73), http.StatusBadRequest},
		{"8字节", "12345678", http.StatusOK},
		{"72字节", strings.Repeat("a", 72), http.StatusOK},
		{"9字节3rune", "密码密", http.StatusOK}, // 3 rune × 3 字节 = 9 字节 ≥ 8
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, dbPath := m6Handler(t)
			email := fmt.Sprintf("m53-len-%d@example.com", i)
			token := m53Register(t, h, dbPath, email)

			status, body := m53Change(h, token, m6Password, tc.pw)
			if status != tc.want {
				t.Fatalf("newPassword=%q(%d 字节) 状态码 = %d, 期望 %d; body=%v",
					tc.pw, len(tc.pw), status, tc.want, body)
			}
			switch tc.want {
			case http.StatusBadRequest:
				expectError(t, status, body, http.StatusBadRequest, tc.name)
				if ver := m53PassVer(t, dbPath, email); ver != 0 {
					t.Errorf("%s 被拒后 pass_ver = %d, 期望 0", tc.name, ver)
				}
				if st, _ := m6Login(h, email, m6Password); st != http.StatusOK {
					t.Errorf("%s 被拒后原密码 login 状态码 = %d, 期望 200", tc.name, st)
				}
			case http.StatusOK:
				if ver := m53PassVer(t, dbPath, email); ver != 1 {
					t.Errorf("%s 成功后 pass_ver = %d, 期望 1", tc.name, ver)
				}
				tok := m53MustLogin(t, h, email, tc.pw, tc.name+"后")
				if st, _, body := m6Roster(h, tok); st != http.StatusOK {
					t.Errorf("%s 新密码 token 调 roster 状态码 = %d, 期望 200; body=%v", tc.name, st, body)
				}
			}
		})
	}
}

// ---------- T5 缺字段 / 坏 JSON ----------

// T5 请求体不合法 → 400：缺 oldPassword、缺 newPassword、空对象、坏 JSON；
// 且一律无副作用（pass_ver 仍 0、原密码仍可登录）。
// 命令: go test ./server/ -run TestM53ChangePasswordBadRequest -v
func TestM53ChangePasswordBadRequest(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "m53-badreq@example.com"
	token := m53Register(t, h, dbPath, email)

	cases := []struct {
		name string
		body map[string]string
	}{
		{"缺oldPassword", map[string]string{"newPassword": m53NewPass}},
		{"缺newPassword", map[string]string{"oldPassword": m6Password}},
		{"空对象", map[string]string{}},
	}
	for _, tc := range cases {
		status, body, _ := m6Do(h, http.MethodPost, m53Path, token, tc.body)
		expectError(t, status, body, http.StatusBadRequest, tc.name)
	}
	if status, body := m53ChangeRaw(h, token, `{"oldPassword": "no-close"`); status != http.StatusBadRequest {
		t.Errorf("坏 JSON 状态码 = %d, 期望 400; body=%v", status, body)
	}

	// 无副作用
	if ver := m53PassVer(t, dbPath, email); ver != 0 {
		t.Errorf("坏请求后 pass_ver = %d, 期望 0", ver)
	}
	m53MustLogin(t, h, email, m6Password, "坏请求后")
}

// ---------- T6 鉴权矩阵 ----------

// T6 无 token → 401；坏 token（末位篡改）→ 401；学生两段式 token（合法 HMAC、
// 无 T 前缀）→ 403（角色不符）；且均无副作用。
// 命令: go test ./server/ -run TestM53ChangePasswordAuthMatrix -v
func TestM53ChangePasswordAuthMatrix(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "m53-auth@example.com"
	token := m53Register(t, h, dbPath, email)
	req := map[string]string{"oldPassword": m6Password, "newPassword": m53NewPass}

	status, body, _ := m6Do(h, http.MethodPost, m53Path, "", req)
	expectError(t, status, body, http.StatusUnauthorized, "无token")

	status, body, _ = m6Do(h, http.MethodPost, m53Path, m6BadToken(token), req)
	expectError(t, status, body, http.StatusUnauthorized, "坏token")

	status, body, _ = m6Do(h, http.MethodPost, m53Path, m53StudentToken(t, dbPath), req)
	expectError(t, status, body, http.StatusForbidden, "学生token")

	if ver := m53PassVer(t, dbPath, email); ver != 0 {
		t.Errorf("鉴权失败后 pass_ver = %d, 期望 0", ver)
	}
}

// ---------- T7 方法级 405 ----------

// T7 GET/PUT/DELETE /api/teacher/change-password → 405 + Allow 头
// （沿用 M2/M4/M6 显式注册模式，405 在鉴权前返回；此处仍带合法 token 发请求）。
// 命令: go test ./server/ -run TestM53ChangePasswordMethodNotAllowed -v
func TestM53ChangePasswordMethodNotAllowed(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m53Register(t, h, dbPath, "m53-405@example.com")
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		resp := performJSON(h, method, m53Path, token, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s 状态码 = %d, 期望 405", method, m53Path, resp.StatusCode)
		} else if resp.Header.Get("Allow") == "" {
			t.Errorf("%s %s 405 响应缺少 Allow 头", method, m53Path)
		}
		resp.Body.Close()
	}
}

// ---------- T8 连续两次改密 ----------

// T8 第二次改密以第一次的新密码为 oldPassword → 200，pass_ver=2；三代密码中
// 仅最新者可登录；第一次改密后签发的 token 随第二次改密失效。
// 命令: go test ./server/ -run TestM53ChangePasswordTwiceIncrementsVer -v
func TestM53ChangePasswordTwiceIncrementsVer(t *testing.T) {
	h, dbPath := m6Handler(t)
	email := "m53-twice@example.com"
	token := m53Register(t, h, dbPath, email)
	const secondPass = "second-pass-456"

	if status, body := m53Change(h, token, m6Password, m53NewPass); status != http.StatusOK {
		t.Fatalf("第一次改密状态码 = %d, 期望 200; body=%v", status, body)
	}
	if ver := m53PassVer(t, dbPath, email); ver != 1 {
		t.Errorf("第一次改密后 pass_ver = %d, 期望 1", ver)
	}
	tok2 := m53MustLogin(t, h, email, m53NewPass, "第一次改密后")

	if status, body := m53Change(h, tok2, m53NewPass, secondPass); status != http.StatusOK {
		t.Fatalf("第二次改密状态码 = %d, 期望 200; body=%v", status, body)
	}
	if ver := m53PassVer(t, dbPath, email); ver != 2 {
		t.Errorf("第二次改密后 pass_ver = %d, 期望 2", ver)
	}

	for _, tc := range []struct {
		pw   string
		want int
	}{
		{m6Password, http.StatusUnauthorized},
		{m53NewPass, http.StatusUnauthorized},
		{secondPass, http.StatusOK},
	} {
		if status, body := m6Login(h, email, tc.pw); status != tc.want {
			t.Errorf("第二次改密后 login(%q) 状态码 = %d, 期望 %d; body=%v", tc.pw, status, tc.want, body)
		}
	}
	// 第一次改密签发的 token 随第二次改密（ver 1→2）失效
	m53ExpectStale(t, h, tok2, "第一次改密token(第二次改密后)")
	tok3 := m53MustLogin(t, h, email, secondPass, "第二次改密后")
	if status, _, body := m6Roster(h, tok3); status != http.StatusOK {
		t.Errorf("第二次改密后新 token 调 roster 状态码 = %d, 期望 200; body=%v", status, body)
	}
}

// ---------- T9 无回归（守卫用例，实现前后都应绿） ----------

// T9 学生侧端点保持 404（不得因本次改动复活）；既有教师端点正常
// （登录态下新增学生、roster 返回该学生）。
// 命令: go test ./server/ -run TestM53NoRegressionStudentEndpointsStayGone -v
func TestM53NoRegressionStudentEndpointsStayGone(t *testing.T) {
	h, dbPath := m6Handler(t)
	token := m53Register(t, h, dbPath, "m53-regress@example.com")

	for _, path := range []string{"/api/dex", "/api/pet/me", "/api/join", "/api/points"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			var body any
			if method == http.MethodPost {
				body = map[string]string{"x": "y"}
			}
			if code := m6Status(h, method, path, token, body); code != http.StatusNotFound {
				t.Errorf("%s %s 状态码 = %d, 期望 404（学生侧仍下线）", method, path, code)
			}
		}
	}

	m6MustCreateStudent(t, h, token, "回归生", "01")
	if status, entries, body := m6Roster(h, token); status != http.StatusOK || len(entries) != 1 {
		t.Errorf("roster = (status=%d, entries=%d), 期望 (200, 1); body=%v", status, len(entries), body)
	}
}
