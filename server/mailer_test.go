package server_test

// server/mailer.go（M6 #27）测试契约 T1–T10。
// 全部用例黑盒、零外网：SMTP 交互经 net.Pipe 假服务器脚本应答。

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/smtp"
	"os"
	"strings"
	"testing"

	"github.com/AI-BBM/electronic-pet/server"
)

// T1 接口契约：两个实现均满足 server.Mailer（签名偏差在编译期暴露）。
// 命令： go test ./server -run '^TestMailer_InterfaceSatisfied$' -v
var (
	_ server.Mailer = (*server.SMTPMailer)(nil)
	_ server.Mailer = (*server.MockMailer)(nil)
)

func TestMailer_InterfaceSatisfied(t *testing.T) {
	var m server.Mailer = server.NewMailer()
	if m == nil {
		t.Fatal("NewMailer() 返回 nil")
	}
}

// clearSMTPEnv 五个 SMTP_* 变量全部置空（t.Setenv 自动恢复），防空值穿透。
func clearSMTPEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM"} {
		t.Setenv(k, "")
	}
}

// setFullSMTPEnv 五项设为合法值；except 非空时把该项置空（模拟部分缺失）。
func setFullSMTPEnv(t *testing.T, except string) {
	t.Helper()
	vals := map[string]string{
		"SMTP_HOST": "smtp.example.com",
		"SMTP_PORT": "465",
		"SMTP_USER": "mailer-user",
		"SMTP_PASS": "mailer-pass",
		"SMTP_FROM": "noreply@example.com",
	}
	for k, v := range vals {
		if k == except {
			v = ""
		}
		t.Setenv(k, v)
	}
}

// T2 环境变量全缺 → 回退 mock，Send 返回 nil 且验证码写标准日志。
// 命令： go test ./server -run '^TestMailer_NoEnv_ReturnsMock$' -v
func TestMailer_NoEnv_ReturnsMock(t *testing.T) {
	clearSMTPEnv(t)
	m, ok := server.NewMailer().(*server.MockMailer)
	if !ok {
		t.Fatalf("期望 *server.MockMailer，实际 %T", server.NewMailer())
	}
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if err := m.SendVerificationCode("student@test.cn", "654321"); err != nil {
		t.Fatalf("mock 发送应返回 nil，实际 %v", err)
	}
	if !strings.Contains(buf.String(), "student@test.cn") || !strings.Contains(buf.String(), "654321") {
		t.Fatalf("日志应含 email 与 code，实际：%q", buf.String())
	}
}

// T3 环境变量部分缺失 → 仍回退 mock（表驱动：缺一即降级）。
// 命令： go test ./server -run '^TestMailer_PartialEnv_FallsBackToMock$' -v
func TestMailer_PartialEnv_FallsBackToMock(t *testing.T) {
	cases := []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM", "ONLY_HOST"}
	for _, missing := range cases {
		t.Run(missing, func(t *testing.T) {
			clearSMTPEnv(t)
			if missing == "ONLY_HOST" {
				t.Setenv("SMTP_HOST", "smtp.example.com")
			} else {
				setFullSMTPEnv(t, missing)
			}
			m, ok := server.NewMailer().(*server.MockMailer)
			if !ok {
				t.Fatalf("缺 %s 时期望回退 mock，实际 %T", missing, server.NewMailer())
			}
			if err := m.SendVerificationCode("a@b.c", "111111"); err != nil {
				t.Fatalf("mock 发送应返回 nil，实际 %v", err)
			}
		})
	}
}

// T4 环境变量齐备 → 返回 SMTPMailer，构造期零网络（懒连接）。
// 命令： go test ./server -run '^TestMailer_FullEnv_ReturnsSMTPMailer_NoNetwork$' -v
func TestMailer_FullEnv_ReturnsSMTPMailer_NoNetwork(t *testing.T) {
	clearSMTPEnv(t)
	// 127.0.0.1:1 必然 connection refused：若构造期偷懒拨号会立刻暴露。
	setFullSMTPEnv(t, "")
	t.Setenv("SMTP_HOST", "127.0.0.1")
	t.Setenv("SMTP_PORT", "1")
	m, ok := server.NewMailer().(*server.SMTPMailer)
	if !ok {
		t.Fatalf("五项齐备期望 *server.SMTPMailer，实际 %T", server.NewMailer())
	}
	if m.Host != "127.0.0.1" || m.Port != 1 {
		t.Fatalf("配置未正确装配：host=%q port=%d", m.Host, m.Port)
	}
}

// fakeSMTPRecorder 记录假服务器收到的命令与 DATA 载荷。
type fakeSMTPRecorder struct {
	addrs     []string
	tlsFlags  []bool
	authLine  string
	mailFrom  string
	rcptTo    string
	dataLines []string
}

// newFakeSMTP 建一条 net.Pipe 假 SMTP 会话：返回注入用 Dial 与记录器。
// 客户端 host 固定传 "localhost"，使 net/smtp PlainAuth 的非 TLS 护栏放行。
// 命令： 见 T5/T6/T7。
func newFakeSMTP(t *testing.T) (dial func(addr string, implicitTLS bool) (*smtp.Client, error), rec *fakeSMTPRecorder) {
	t.Helper()
	rec = &fakeSMTPRecorder{}
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { serverConn.Close(); clientConn.Close() })
	go func() {
		defer serverConn.Close()
		runFakeSMTPSession(serverConn, rec)
	}()
	return func(addr string, implicitTLS bool) (*smtp.Client, error) {
		rec.addrs = append(rec.addrs, addr)
		rec.tlsFlags = append(rec.tlsFlags, implicitTLS)
		return smtp.NewClient(clientConn, "localhost")
	}, rec
}

// runFakeSMTPSession 按 SMTP 脚本应答并记录命令（读一行答一行）。
func runFakeSMTPSession(rw net.Conn, rec *fakeSMTPRecorder) {
	mustWrite(rw, "220 fake ESMTP ready\r\n")
	r := bufio.NewReader(rw)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			mustWrite(rw, "250-fake\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(line, "AUTH PLAIN"):
			rec.authLine = line
			mustWrite(rw, "235 ok\r\n")
		case strings.HasPrefix(line, "MAIL FROM:"):
			rec.mailFrom = line
			mustWrite(rw, "250 ok\r\n")
		case strings.HasPrefix(line, "RCPT TO:"):
			rec.rcptTo = line
			mustWrite(rw, "250 ok\r\n")
		case line == "DATA":
			mustWrite(rw, "354 end with <CRLF>.<CRLF>\r\n")
			if err := readDotPayload(r, rec); err != nil {
				return
			}
			mustWrite(rw, "250 ok\r\n")
		case strings.HasPrefix(line, "QUIT"):
			mustWrite(rw, "221 bye\r\n")
			return
		default:
			mustWrite(rw, "250 ok\r\n")
		}
	}
}

func mustWrite(w io.Writer, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		panic(err)
	}
}

// readDotPayload 原始收集 DATA 载荷至独占一行的 "."（本套正文无前导点，免去
// 去点 stuffed 处理）。
func readDotPayload(r *bufio.Reader, rec *fakeSMTPRecorder) error {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimRight(line, "\r\n") == "." {
			return nil
		}
		rec.dataLines = append(rec.dataLines, line)
	}
}

// T5 Dial 地址拼接与 TLS 模式：465 隐式 TLS，非 465 走 STARTTLS。
// 命令： go test ./server -run '^TestMailer_SMTP_DialAddrAndTLSMode$' -v
func TestMailer_SMTP_DialAddrAndTLSMode(t *testing.T) {
	cases := []struct {
		name         string
		port         int
		wantAddr     string
		wantImplicit bool
	}{
		{"465-implicit-tls", 465, "smtp.example.com:465", true},
		{"587-starttls", 587, "smtp.example.com:587", false},
		{"25-starttls", 25, "smtp.example.com:25", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dial, rec := newFakeSMTP(t)
			m := server.NewSMTPMailer("smtp.example.com", tc.port, "u", "p", "noreply@example.com")
			m.Dial = dial
			if err := m.SendVerificationCode("to@test.cn", "222222"); err != nil {
				t.Fatalf("发信应成功，实际 %v", err)
			}
			if len(rec.addrs) != 1 || rec.addrs[0] != tc.wantAddr {
				t.Fatalf("Dial addr 期望 %q，实际 %v", tc.wantAddr, rec.addrs)
			}
			if len(rec.tlsFlags) != 1 || rec.tlsFlags[0] != tc.wantImplicit {
				t.Fatalf("implicitTLS 期望 %v，实际 %v", tc.wantImplicit, rec.tlsFlags)
			}
		})
	}
}

// T6 SMTP 报文内容：头字段、UTF-8 MIME、验证码在正文段、信封地址精确。
// 命令： go test ./server -run '^TestMailer_SMTP_MessageContent$' -v
func TestMailer_SMTP_MessageContent(t *testing.T) {
	dial, rec := newFakeSMTP(t)
	m := server.NewSMTPMailer("smtp.example.com", 587, "u", "p", "noreply@example.com")
	m.Dial = dial
	if err := m.SendVerificationCode("to@test.cn", "738921"); err != nil {
		t.Fatalf("发信应成功，实际 %v", err)
	}
	data := strings.Join(rec.dataLines, "")

	if rec.mailFrom != "MAIL FROM:<noreply@example.com>" {
		t.Errorf("信封 MAIL FROM 期望尖括号格式，实际 %q", rec.mailFrom)
	}
	if rec.rcptTo != "RCPT TO:<to@test.cn>" {
		t.Errorf("信封 RCPT TO 期望尖括号格式，实际 %q", rec.rcptTo)
	}
	if !strings.Contains(data, "From: noreply@example.com\r\n") {
		t.Errorf("载荷应含 From 头，实际 %q", data)
	}
	if !strings.Contains(data, "To: to@test.cn\r\n") {
		t.Errorf("载荷应含 To 头，实际 %q", data)
	}
	hasSubject := false
	for _, l := range rec.dataLines {
		if s, ok := strings.CutPrefix(l, "Subject:"); ok && strings.TrimSpace(s) != "" {
			hasSubject = true
		}
	}
	if !hasSubject {
		t.Errorf("载荷应含非空 Subject 行，实际 %q", data)
	}
	if !strings.Contains(data, "MIME-Version: 1.0\r\n") {
		t.Errorf("载荷应含 MIME-Version，实际 %q", data)
	}
	if !strings.Contains(strings.ToLower(data), "charset=utf-8") {
		t.Errorf("Content-Type 应声明 UTF-8，实际 %q", data)
	}
	sep := strings.Index(data, "\r\n\r\n")
	if sep < 0 {
		t.Fatalf("载荷应含头/正文空行分隔，实际 %q", data)
	}
	bodySection := data[sep+4:]
	if !strings.Contains(bodySection, "738921") {
		t.Errorf("验证码应出现在正文段，实际正文 %q", bodySection)
	}
}

// T7 AUTH PLAIN 使用注入凭据（\x00user\x00pass，identity 空）。
// 命令： go test ./server -run '^TestMailer_SMTP_AuthPlainCredentials$' -v
func TestMailer_SMTP_AuthPlainCredentials(t *testing.T) {
	dial, rec := newFakeSMTP(t)
	m := server.NewSMTPMailer("smtp.example.com", 587, "mailer-user", "mailer-pass", "noreply@example.com")
	m.Dial = dial
	if err := m.SendVerificationCode("to@test.cn", "444444"); err != nil {
		t.Fatalf("发信应成功，实际 %v", err)
	}
	const prefix = "AUTH PLAIN "
	if !strings.HasPrefix(rec.authLine, prefix) {
		t.Fatalf("期望 AUTH PLAIN 命令，实际 %q", rec.authLine)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(rec.authLine, prefix))
	if err != nil {
		t.Fatalf("AUTH 参数应为 base64，实际 %q：%v", rec.authLine, err)
	}
	if want := "\x00mailer-user\x00mailer-pass"; string(raw) != want {
		t.Fatalf("PLAIN 凭据期望 %q，实际 %q", want, raw)
	}
}

// T8 Dial 失败错误传播：不吞错不 panic。
// 命令： go test ./server -run '^TestMailer_SMTP_DialErrorPropagates$' -v
func TestMailer_SMTP_DialErrorPropagates(t *testing.T) {
	m := server.NewSMTPMailer("smtp.example.com", 465, "u", "p", "noreply@example.com")
	m.Dial = func(string, bool) (*smtp.Client, error) { return nil, errors.New("dial boom") }
	err := m.SendVerificationCode("to@test.cn", "000000")
	if err == nil {
		t.Fatal("Dial 失败应返回错误")
	}
	if !strings.Contains(err.Error(), "dial boom") {
		t.Fatalf("错误应保留根因，实际 %v", err)
	}
}

// T9 非法端口（非数字/越界）→ 回退 mock，服务保持可用。
// 命令： go test ./server -run '^TestMailer_InvalidPort_FallsBackToMock$' -v
func TestMailer_InvalidPort_FallsBackToMock(t *testing.T) {
	for _, port := range []string{"abc", "58.7", "0", "-1", "70000"} {
		t.Run(port, func(t *testing.T) {
			clearSMTPEnv(t)
			setFullSMTPEnv(t, "")
			t.Setenv("SMTP_PORT", port)
			m, ok := server.NewMailer().(*server.MockMailer)
			if !ok {
				t.Fatalf("非法端口 %q 期望回退 mock，实际 %T", port, server.NewMailer())
			}
			if err := m.SendVerificationCode("a@b.c", "111111"); err != nil {
				t.Fatalf("mock 发送应返回 nil，实际 %v", err)
			}
		})
	}
}

// T10 回归：全量测试绿、vet 干净、gofmt 干净、改动仅两个新文件。
// 命令： go test ./... && go vet ./... && gofmt -l server/ && git status --porcelain
func TestMailer_RepoRegression(t *testing.T) {
	t.Log("本用例为占位标记：T10 的四条命令由交付流程在仓库根执行并留存真实输出")
}
