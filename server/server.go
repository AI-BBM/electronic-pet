package server

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AI-BBM/electronic-pet/web"
)

type ctxKey int

const (
	ctxKeyStudent ctxKey = iota // M6 起仅保留鉴权语义（学生存量 token 打教师端点 403）
	ctxKeyTeacher
)

type srv struct {
	db          *sql.DB
	dbMu        sync.Mutex // 串行化 DB 访问（单连接 SQLite，事务内互斥）
	tokenSecret string
	levels      LevelConfig // 升级阈值，启动时可经 PET_LEVELS_FILE 覆盖
	mailer      Mailer      // 验证码发信（SMTP 未配置时为 mock）
}

// New 构建完整路由（M6 纯教师侧）。同一 dbPath 可重复调用（幂等迁移、密钥复用）。
// 返回值实现 Close() error，调用方（含测试）用后应释放 SQLite 句柄。
func New(dbPath string) (http.Handler, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	secret, err := loadOrCreateTokenSecret(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	levels := DefaultLevels()
	if path := os.Getenv(LevelsFileEnv); path != "" {
		if levels, err = LoadLevels(path); err != nil {
			db.Close()
			return nil, err
		}
	}
	s := &srv{db: db, tokenSecret: secret, levels: levels, mailer: NewMailerFromEnv()}
	s.cleanupExpiredStudents(time.Now()) // 启动清理垃圾桶超期数据
	s.startTrashCleanupLoop()
	return &appHandler{srv: s, mux: s.routes()}, nil
}

// appHandler 包装路由 mux，并暴露 Close 释放 DB 句柄。
type appHandler struct {
	srv *srv
	mux http.Handler
}

func (a *appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.mux.ServeHTTP(w, r) }

// Close 释放底层 SQLite 连接。
func (a *appHandler) Close() error { return a.srv.db.Close() }

func (s *srv) routes() http.Handler {
	mux := http.NewServeMux()
	taught := s.requireTeacher

	// 免鉴权：教师注册 / 登录 / 发验证码
	mux.HandleFunc("POST /api/teacher/email-code", s.handleTeacherEmailCode)
	mux.HandleFunc("POST /api/teacher/register", s.handleTeacherRegister)
	mux.HandleFunc("POST /api/teacher/login", s.handleTeacherLogin)

	// 名单管理（教师鉴权）
	mux.Handle("POST /api/teacher/students", taught(http.HandlerFunc(s.handleCreateStudent)))
	mux.Handle("PATCH /api/teacher/students/{id}", taught(http.HandlerFunc(s.handlePatchStudent)))
	mux.Handle("DELETE /api/teacher/students/{id}", taught(http.HandlerFunc(s.handleDeleteStudent)))
	mux.Handle("GET /api/teacher/trash", taught(http.HandlerFunc(s.handleTeacherTrash)))
	mux.Handle("POST /api/teacher/students/{id}/restore", taught(http.HandlerFunc(s.handleRestoreStudent)))
	mux.Handle("POST /api/teacher/pets/{studentID}/name", taught(http.HandlerFunc(s.handleTeacherRenamePet)))

	// M4 契约沿用：代发宠物 / 代加分 / 花名册
	mux.Handle("POST /api/teacher/adopt", taught(http.HandlerFunc(s.handleTeacherAdopt)))
	mux.Handle("POST /api/teacher/points", taught(http.HandlerFunc(s.handleTeacherPoints)))
	mux.Handle("GET /api/teacher/roster", taught(http.HandlerFunc(s.handleTeacherRoster)))

	// 方法级 405（沿用 M2/M4 模式；Allow 头在鉴权前返回）
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		mux.HandleFunc(m+" /api/teacher/email-code", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/register", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/login", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/adopt", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/points", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/pets/{studentID}/name", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/students/{id}/restore", s.methodNotAllowed(http.MethodPost))
	}
	mux.HandleFunc("GET /api/teacher/students", s.methodNotAllowed(http.MethodPost))
	mux.HandleFunc("PUT /api/teacher/students", s.methodNotAllowed(http.MethodPost))
	mux.HandleFunc("PATCH /api/teacher/students", s.methodNotAllowed(http.MethodPost))
	mux.HandleFunc("DELETE /api/teacher/students", s.methodNotAllowed(http.MethodPost))
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
		mux.HandleFunc(m+" /api/teacher/students/{id}", s.methodNotAllowed(http.MethodPatch+", "+http.MethodDelete))
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		mux.HandleFunc(m+" /api/teacher/trash", s.methodNotAllowed(http.MethodGet))
		mux.HandleFunc(m+" /api/teacher/roster", s.methodNotAllowed(http.MethodGet))
	}

	// 学生侧已下线（M6）：/api/* 其余路径一律 JSON 404（不能落入 SPA 回退）。
	// M6-T10 契约：join/eggs/adopt/pet/me/name/points/log/dex/wall 等 GET/POST 均 404。
	// 逐方法注册（无方法 pattern 与 "GET /" 会触发 ServeMux 冲突 panic）。
	for _, m := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		mux.HandleFunc(m+" /api/", s.handleAPIGone)
	}

	mux.Handle("GET /", s.staticHandler())
	return mux
}

// handleAPIGone 学生侧端点与未知 API 路径的统一 404（带 JSON error）。
func (s *srv) handleAPIGone(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, errJSON("接口不存在"))
}

// requireTeacher 校验教师 Bearer token，把 classID 注入 context；
// 学生 token（M6 起已无合法签发途径，存量自然失效）角色不符 403。
func (s *srv) requireTeacher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		classID, err := verifyTeacherToken(s.tokenSecret, token)
		if errors.Is(err, errWrongRole) {
			writeJSON(w, http.StatusForbidden, errJSON("权限不足"))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errJSON("未登录或 token 无效"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyTeacher, classID)))
	})
}

// methodNotAllowed 返回固定允许方法的 405 处理器（鉴权前拦截）。
func (s *srv) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeJSON(w, http.StatusMethodNotAllowed, errJSON("方法不允许"))
	}
}

// staticHandler 服务内嵌前端；默认页与 SPA 回退均为教师工作台（M6 起 / 即教师端）。
func (s *srv) staticHandler() http.Handler {
	sub, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic(err) // embed 内容编译期固定，不可能失败
	}
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, errJSON("method not allowed"))
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			// 根路径直接直出教师工作台（FileServerFS 对 "/" 只认 index.html）
			index, err := fs.ReadFile(sub, "teacher.html")
			if err != nil {
				http.Error(w, "teacher.html missing", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// SPA 回退：非文件路径一律回教师工作台
			index, err := fs.ReadFile(sub, "teacher.html")
			if err != nil {
				http.Error(w, "teacher.html missing", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
