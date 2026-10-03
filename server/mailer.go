package server

import (
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Mailer 验证码邮件器（M6 班主任邮箱注册发信用）。
type Mailer interface {
	SendVerificationCode(email, code string) error
}

// SMTP_* 环境变量（凭据由部署环境注入，不入仓库）。
const (
	envSMTPHost = "SMTP_HOST"
	envSMTPPort = "SMTP_PORT"
	envSMTPUser = "SMTP_USER"
	envSMTPPass = "SMTP_PASS"
	envSMTPFrom = "SMTP_FROM"
)

// 编译期接口契约。
var (
	_ Mailer = (*SMTPMailer)(nil)
	_ Mailer = (*MockMailer)(nil)
)

// SMTPDialFunc 建立 SMTP 会话连接；implicitTLS 为 true 时对 addr 发起隐式 TLS
// （465），否则明文连接后由调用方 STARTTLS。导出仅为测试注入，生产用默认实现。
type SMTPDialFunc func(addr string, implicitTLS bool) (*smtp.Client, error)

// SMTPMailer 直连 SMTP 服务（阿里云 DirectMail 兼容：465 隐式 TLS / 其他端口
// STARTTLS + AUTH PLAIN）。
type SMTPMailer struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// Dial 网络出口，nil 时用默认实现；测试注入假连接避免触网。
	Dial SMTPDialFunc
}

// MockMailer 未配置 SMTP_* 时的降级实现：验证码写标准日志，服务保持可用。
type MockMailer struct{}

// NewMailer 按环境变量装配邮件器：五项配置齐备且端口合法返回 SMTPMailer，
// 否则一律降级 MockMailer（构造期零网络，任何失败形态不影响服务可用）。
func NewMailer() Mailer {
	host := os.Getenv(envSMTPHost)
	portStr := os.Getenv(envSMTPPort)
	user := os.Getenv(envSMTPUser)
	pass := os.Getenv(envSMTPPass)
	from := os.Getenv(envSMTPFrom)
	if host == "" || portStr == "" || user == "" || pass == "" || from == "" {
		return &MockMailer{}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return &MockMailer{}
	}
	return NewSMTPMailer(host, port, user, pass, from)
}

// NewSMTPMailer 用显式配置构造 SMTP 邮件器（测试绕过环境变量直接构造）。
func NewSMTPMailer(host string, port int, username, password, from string) *SMTPMailer {
	return &SMTPMailer{Host: host, Port: port, Username: username, Password: password, From: from}
}

// SendVerificationCode 向 email 发送含 code 的验证码邮件。
// 加密由 Dial 契约保证：默认实现 465 走隐式 TLS，其余端口强制 STARTTLS（未通告
// 即拒绝，绝不明文发凭据）；注入的自定义 Dial 视同已提供加密会话。
// Hello 单一归属：默认 dialSMTP 的 STARTTLS 分支已 Hello，其余路径靠 net/smtp
// 惰性 hello（localName 默认 localhost），此处不得再显式调用。
func (m *SMTPMailer) SendVerificationCode(email, code string) error {
	if strings.ContainsAny(email, "\r\n") || strings.ContainsAny(m.From, "\r\n") {
		return errors.New("mailer: email/from 含换行符，拒绝疑似头注入的地址")
	}
	implicit := m.Port == 465
	dial := m.Dial
	if dial == nil {
		dial = dialSMTP
	}
	cli, err := dial(fmt.Sprintf("%s:%d", m.Host, m.Port), implicit)
	if err != nil {
		return fmt.Errorf("mailer: dial %s:%d: %w", m.Host, m.Port, err)
	}
	defer cli.Close()

	if err := cli.Auth(tlsPlainAuth{username: m.Username, password: m.Password}); err != nil {
		return fmt.Errorf("mailer: auth: %w", err)
	}
	if err := cli.Mail(m.From); err != nil {
		return fmt.Errorf("mailer: mail from: %w", err)
	}
	if err := cli.Rcpt(email); err != nil {
		return fmt.Errorf("mailer: rcpt: %w", err)
	}
	w, err := cli.Data()
	if err != nil {
		return fmt.Errorf("mailer: data: %w", err)
	}
	if _, err := w.Write(buildVerificationMail(m.From, email, code)); err != nil {
		w.Close()
		return fmt.Errorf("mailer: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: close body: %w", err)
	}
	return cli.Quit()
}

// SendVerificationCode 降级实现：验证码写标准日志（口径与 #25 M6 钉死一致：
// 前缀 [mock-mail]），服务保持可用。
func (m *MockMailer) SendVerificationCode(email, code string) error {
	log.Printf("[mock-mail] to=%s code=%s", email, code)
	return nil
}

// tlsPlainAuth SMTP PLAIN 机制凭据（\x00user\x00pass，identity 空）。
// 不用 smtp.PlainAuth：其安全检查一依赖 Client.tls（465 隐式 TLS 拨号不会置位，
// 会被误判明文连接拒发凭据）、二绑定制服 host（注入假连接的测试无法对齐）。
// 本实现的加密前提由 Dial 契约保证，见 SendVerificationCode 注释。
type tlsPlainAuth struct {
	username string
	password string
}

func (a tlsPlainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a tlsPlainAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("mailer: unexpected server challenge: %q", fromServer)
	}
	// more=false：服务器已回 235 接受，返回 (nil, nil) 结束 AUTH 会话。
	return nil, nil
}

// dialSMTP 默认网络出口：465 隐式 TLS；其他端口明文连接后必须成功 STARTTLS，
// 服务器未通告 STARTTLS 时拒绝（fail-closed），绝不回退明文会话。
func dialSMTP(addr string, implicitTLS bool) (*smtp.Client, error) {
	host := hostOf(addr)
	if implicitTLS {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return nil, err
		}
		return smtp.NewClient(conn, host)
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	cli, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := cli.Hello("localhost"); err != nil {
		cli.Close()
		return nil, err
	}
	if ok, _ := cli.Extension("STARTTLS"); !ok {
		cli.Close()
		return nil, fmt.Errorf("mailer: %s 未通告 STARTTLS，拒绝明文发送凭据（请改用 465 端口）", addr)
	}
	if err := cli.StartTLS(&tls.Config{ServerName: host}); err != nil {
		cli.Close()
		return nil, err
	}
	return cli, nil
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// buildVerificationMail 构造 RFC 5322 验证码邮件（头 + 空行 + UTF-8 正文）。
func buildVerificationMail(from, to, code string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: 宠物班级验证码\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString("你的验证码是：" + code + "（10 分钟内有效，请勿泄露）。\r\n")
	return []byte(b.String())
}

// ---------- M6 验证码业务层（来自 #25 分支，传输层复用上方 SMTPMailer） ----------

// 验证码规则（PRD M6）：6 位数字、10 分钟有效、同邮箱 60s 限速、每日上限 10 次。
// 存储走 meta 表（单实例部署约束），键约定：
//
//	email_code:<email>      = <code>|<expiresUnix>
//	email_code_sent:<email> = <上次发送 unix 秒>
//	email_code_cnt:<email>:<YYYYMMDD> = 当日已发次数
const (
	verifyCodeTTL      = 10 * time.Minute
	verifyCodeResend   = 60 * time.Second
	verifyCodeDailyMax = 10
)

var (
	errCodeRateLimited = errors.New("发送过于频繁，请 1 分钟后再试")
	errCodeDailyMax    = errors.New("今日发送次数已达上限，请明日再试")
)

// NewMailerFromEnv 是 NewMailer 的语义别名（#25 注册链路的调用点命名）。
func NewMailerFromEnv() Mailer { return NewMailer() }

// generateVerifyCode 生成 6 位数字验证码（crypto/rand）。
func generateVerifyCode() (string, error) {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// sendEmailCode 校验限速后生成、存储并（经 Mailer）发送验证码。
func (s *srv) sendEmailCode(email string) error {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	now := time.Now()
	var sentAt int64
	_ = s.db.QueryRow(`SELECT v FROM meta WHERE k = ?`, "email_code_sent:"+email).Scan(&sentAt)
	if now.Unix()-sentAt < int64(verifyCodeResend.Seconds()) {
		return errCodeRateLimited
	}
	dayKey := fmt.Sprintf("email_code_cnt:%s:%s", email, now.Format("20060102"))
	var cnt int64
	_ = s.db.QueryRow(`SELECT v FROM meta WHERE k = ?`, dayKey).Scan(&cnt)
	if cnt >= verifyCodeDailyMax {
		return errCodeDailyMax
	}

	code, err := generateVerifyCode()
	if err != nil {
		return err
	}
	expires := now.Add(verifyCodeTTL).Unix()
	value := code + "|" + strconv.FormatInt(expires, 10)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range map[string]string{
		"email_code:" + email:      value,
		"email_code_sent:" + email: strconv.FormatInt(now.Unix(), 10),
		dayKey:                     strconv.FormatInt(cnt+1, 10),
	} {
		if _, err := tx.Exec(
			`INSERT INTO meta(k, v) VALUES(?, ?)
			 ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.mailer.SendVerificationCode(email, code)
}

// checkEmailCode 校验验证码（存在、未过期、匹配）。用后即删，防重放。
func (s *srv) checkEmailCode(email, code string) error {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	return checkEmailCodeDB(s.db, email, code)
}

// dbExecQuerier 抽象 *sql.DB 与 *sql.Tx 共有的查询/执行能力（校验码用后即删需在同一事务内）。
type dbExecQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// checkEmailCodeDB 校验验证码（存在、未过期、匹配、未超失败上限）。用后即删，防重放；
// 连续失败达 verifyCodeMaxAttempts 直接作废该码（防 6 位码空间被暴力猜测）。
func checkEmailCodeDB(db dbExecQuerier, email, code string) error {
	const (
		keyFmt              = "email_code:"
		failKeyFmt          = "email_code_fail:"
		maxAttempts   int64 = 5
	)
	var value string
	err := db.QueryRow(`SELECT v FROM meta WHERE k = ?`, keyFmt+email).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("验证码不存在或已使用")
	}
	if err != nil {
		return err
	}
	parts := strings.SplitN(value, "|", 2)
	if len(parts) != 2 {
		return errors.New("验证码无效")
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return errors.New("验证码无效")
	}
	if time.Now().Unix() > expires {
		return errors.New("验证码已过期，请重新获取")
	}
	if parts[0] != code {
		var fails int64
		_ = db.QueryRow(`SELECT v FROM meta WHERE k = ?`, failKeyFmt+email).Scan(&fails)
		fails++
		if fails >= maxAttempts {
			// 作废：达上限后连正确码也拒绝
			_, _ = db.Exec(`DELETE FROM meta WHERE k = ?`, keyFmt+email)
			_, _ = db.Exec(`INSERT INTO meta(k, v) VALUES(?, '1')
				ON CONFLICT(k) DO UPDATE SET v = '1'`, failKeyFmt+email)
			return errors.New("验证码错误次数过多，请重新获取")
		}
		_, _ = db.Exec(`INSERT INTO meta(k, v) VALUES(?, ?)
			ON CONFLICT(k) DO UPDATE SET v = excluded.v`, failKeyFmt+email, strconv.FormatInt(fails, 10))
		return errors.New("验证码不正确")
	}
	_, err = db.Exec(`DELETE FROM meta WHERE k = ?`, keyFmt+email)
	if err == nil {
		_, _ = db.Exec(`DELETE FROM meta WHERE k = ?`, failKeyFmt+email)
	}
	return err
}
