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

	"github.com/AI-BBM/electronic-pet/web"
)

type ctxKey int

const (
	ctxKeyStudent ctxKey = iota // int64 studentID
	ctxKeyTeacher               // int64 classID
)

type srv struct {
	db          *sql.DB
	dbMu        sync.Mutex // 串行化 DB 访问（单连接 SQLite，事务内互斥）
	tokenSecret string
	levels      LevelConfig // 升级阈值，启动时可经 PET_LEVELS_FILE 覆盖（M2）
}

// New 构建完整路由（含内嵌前端）。同一 dbPath 可重复调用（幂等建表、密钥复用）。
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
	levels := DefaultLevels()
	if path := os.Getenv(LevelsFileEnv); path != "" {
		if levels, err = LoadLevels(path); err != nil {
			db.Close()
			return nil, err
		}
	}
	s := &srv{db: db, tokenSecret: secret, levels: levels}
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

	// M2 加分与积分流水。其余方法显式注册为 405（方法级 pattern 与 "GET /" 无冲突，
	// 也不能用不带方法的 pattern——它与 "GET /" 互不为子集会 panic）；
	// 流水不可改删（PRD M2），故 /api/points 仅 POST、/api/pet/me/log 仅 GET。
	mux.Handle("POST /api/points", authed(http.HandlerFunc(s.handleAddPoints)))
	mux.Handle("GET /api/pet/me/log", authed(http.HandlerFunc(s.handleLog)))
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		mux.HandleFunc(m+" /api/points", s.methodNotAllowed(http.MethodPost))
	}
	for _, m := range []string{http.MethodPut, http.MethodDelete} {
		mux.HandleFunc(m+" /api/pet/me/log", s.methodNotAllowed(http.MethodGet))
	}

	// M3 图鉴与班级墙（只读端点）。非 GET 方法显式注册为 JSON 405，
	// 与全局语义一致（否则 ServeMux 默认 405 为纯文本）。
	mux.Handle("GET /api/dex", authed(http.HandlerFunc(s.handleDex)))
	mux.Handle("GET /api/class/wall", authed(http.HandlerFunc(s.handleWall)))
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		mux.HandleFunc(m+" /api/dex", s.methodNotAllowed(http.MethodGet))
		mux.HandleFunc(m+" /api/class/wall", s.methodNotAllowed(http.MethodGet))
	}

	// M4 教师端。登录口免鉴权；其余教师端点走 requireTeacher（角色隔离，学生 token 403）。
	mux.HandleFunc("POST /api/teacher/login", s.handleTeacherLogin)
	taught := s.requireTeacher
	mux.Handle("POST /api/teacher/adopt", taught(http.HandlerFunc(s.handleTeacherAdopt)))
	mux.Handle("POST /api/teacher/points", taught(http.HandlerFunc(s.handleTeacherPoints)))
	mux.Handle("GET /api/teacher/roster", taught(http.HandlerFunc(s.handleTeacherRoster)))
	mux.Handle("POST /api/teacher/passcode", taught(http.HandlerFunc(s.handleTeacherPasscode)))
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		mux.HandleFunc(m+" /api/teacher/adopt", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/points", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/passcode", s.methodNotAllowed(http.MethodPost))
		mux.HandleFunc(m+" /api/teacher/login", s.methodNotAllowed(http.MethodPost))
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		mux.HandleFunc(m+" /api/teacher/roster", s.methodNotAllowed(http.MethodGet))
	}

	mux.Handle("GET /", s.staticHandler())
	return mux
}

// methodNotAllowed 返回固定允许方法的 405 处理器（鉴权前拦截，M2 流水不可改删）。
func (s *srv) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeJSON(w, http.StatusMethodNotAllowed, errJSON("方法不允许"))
	}
}

// requireAuth 解析 Bearer token 并校验角色；角色不符 403，token 无效 401。
func (s *srv) requireAuth(role string, next func(w http.ResponseWriter, r *http.Request, id int64)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var (
			id  int64
			err error
		)
		if role == roleTeacher {
			id, err = verifyTeacherToken(s.tokenSecret, token)
		} else {
			id, err = verifyToken(s.tokenSecret, token)
		}
		if err != nil {
			if errors.Is(err, errWrongRole) {
				writeJSON(w, http.StatusForbidden, errJSON("角色无权访问该端点"))
				return
			}
			writeJSON(w, http.StatusUnauthorized, errJSON("未登录或 token 无效"))
			return
		}
		key := ctxKeyStudent
		if role == roleTeacher {
			key = ctxKeyTeacher
		}
		next(w, r.WithContext(context.WithValue(r.Context(), key, id)), id)
	})
}

// requireStudent 校验学生 Bearer token，把 studentID 注入 context。
func (s *srv) requireStudent(next http.Handler) http.Handler {
	return s.requireAuth(roleStudent, func(w http.ResponseWriter, r *http.Request, _ int64) {
		next.ServeHTTP(w, r)
	})
}

// requireTeacher 校验教师 Bearer token，把 classID 注入 context（M4 角色隔离）。
func (s *srv) requireTeacher(next http.Handler) http.Handler {
	return s.requireAuth(roleTeacher, func(w http.ResponseWriter, r *http.Request, _ int64) {
		next.ServeHTTP(w, r)
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
