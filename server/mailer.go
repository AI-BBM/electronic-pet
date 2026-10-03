package server

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Mailer 发信接口：M6 用它发注册验证码；#27 交付 SMTP 实现后由 NewMailerFromEnv
// 自动切换，mock 行为（日志口径）保留为未配置时的回退。
type Mailer interface {
	SendVerifyCode(to, code string) error
}

// mockMailer 未配置 SMTP 时的回退实现：验证码记日志（[mock-mail] 前缀，
// M6 测试以此口径拿码），不外发。
type mockMailer struct{}

func (mockMailer) SendVerifyCode(to, code string) error {
	log.Printf("[mock-mail] to=%s code=%s", to, code)
	return nil
}

// smtpMailer 生产实现：SMTP 凭据来自环境变量
// SMTP_HOST/SMTP_PORT/SMTP_USER/SMTP_PASS/SMTP_FROM。
type smtpMailer struct {
	host string
	port string
	user string
	pass string
	from string
}

func (m smtpMailer) SendVerifyCode(to, code string) error {
	addr := m.host + ":" + m.port
	subject := "电子宠物 · 班主任注册验证码"
	body := "你的验证码是 " + code + "，10 分钟内有效。若非本人操作请忽略本邮件。"
	msg := strings.Join([]string{
		"From: " + m.from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")
	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}
	return smtp.SendMail(addr, auth, m.from, []string{to}, []byte(msg))
}

// NewMailerFromEnv 按环境变量构建 Mailer；未配置 SMTP_HOST 时返回 mock。
func NewMailerFromEnv() Mailer {
	if host := os.Getenv("SMTP_HOST"); host != "" {
		port := os.Getenv("SMTP_PORT")
		if port == "" {
			port = "25"
		}
		from := os.Getenv("SMTP_FROM")
		if from == "" {
			from = os.Getenv("SMTP_USER")
		}
		return smtpMailer{host: host, port: port, user: os.Getenv("SMTP_USER"), pass: os.Getenv("SMTP_PASS"), from: from}
	}
	return mockMailer{}
}

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
	return s.mailer.SendVerifyCode(email, code)
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

func checkEmailCodeDB(db dbExecQuerier, email, code string) error {
	var value string
	err := db.QueryRow(`SELECT v FROM meta WHERE k = ?`, "email_code:"+email).Scan(&value)
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
		return errors.New("验证码不正确")
	}
	_, err = db.Exec(`DELETE FROM meta WHERE k = ?`, "email_code:"+email)
	return err
}
