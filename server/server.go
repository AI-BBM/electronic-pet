package server

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/AI-BBM/electronic-pet/web"
)

type ctxKey int

const ctxKeyStudent ctxKey = iota

type srv struct {
	db          *sql.DB
	dbMu        sync.Mutex // 串行化 DB 访问（单连接 SQLite，事务内互斥）
	tokenSecret string
}

// New 构建 M1 完整路由（含内嵌前端）。同一 dbPath 可重复调用（幂等建表、密钥复用）。
// 返回值实现 Close() error，调用方（含测试）用后应释放 SQLite 句柄（Windows 文件锁）。
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
	s := &srv{db: db, tokenSecret: secret}
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

	mux.HandleFunc("POST /api/join", s.handleJoin)

	authed := s.requireStudent
	mux.Handle("GET /api/eggs", authed(http.HandlerFunc(s.handleEggs)))
	mux.Handle("POST /api/adopt", authed(http.HandlerFunc(s.handleAdopt)))
	mux.Handle("GET /api/pet/me", authed(http.HandlerFunc(s.handlePetMe)))
	mux.Handle("POST /api/pet/name", authed(http.HandlerFunc(s.handleRename)))

	mux.Handle("GET /", s.staticHandler())
	return mux
}

// requireStudent 校验 Bearer token，把 studentID 注入 context。
func (s *srv) requireStudent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		studentID, err := verifyToken(s.tokenSecret, token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errJSON("未登录或 token 无效"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyStudent, studentID)))
	})
}

// staticHandler 服务内嵌前端；未命中的非 API 路径回退 index.html（SPA）。
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
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// SPA 回退：非文件路径一律回 index.html
			index, err := fs.ReadFile(sub, "index.html")
			if err != nil {
				http.Error(w, "index.html missing", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
