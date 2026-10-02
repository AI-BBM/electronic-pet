package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 单次加分的分值上下限与理由长度上限。
const (
	minPointsValue = 1
	maxPointsValue = 10
	maxReasonLen   = 100
)

// logPageSize 是积分流水的固定分页大小。
const logPageSize = 20

// maxBodyBytes 限制请求体大小，防滥用。
const maxBodyBytes = 4 << 10

// LevelsFileEnv 指定等级阈值 JSON 文件的环境变量；未设置时用默认阈值 20/60。
const LevelsFileEnv = "PET_LEVELS_FILE"

// New 是生产构造口（骨架期临时实现）：打开库、按环境变量加载阈值、挂载 M2 路由。
// M1 合并 main 后以 M1 的 New() 为准删除本函数，M2 路由经 registerPointsRoutes 挂入。
func New(dbPath string) (http.Handler, error) {
	s, err := OpenStore(dbPath)
	if err != nil {
		return nil, err
	}
	levels := DefaultLevels()
	if path := os.Getenv(LevelsFileEnv); path != "" {
		if levels, err = LoadLevels(path); err != nil {
			s.Close()
			return nil, err
		}
	}
	secret, err := s.secret()
	if err != nil {
		s.Close()
		return nil, err
	}
	return NewMux(s, levels, secret), nil
}

// NewMux 构造仅含 M2 路由的处理器（测试与骨架期独立运行用）；
// 生产 mux 由 M1 的 New() 调 registerPointsRoutes 挂载同一路由。
func NewMux(s *Store, levels LevelConfig, secret []byte) http.Handler {
	mux := http.NewServeMux()
	registerPointsRoutes(mux, s, levels, secret)
	return mux
}

// registerPointsRoutes 把 M2 的加分与流水路由挂到 mux 上（供生产 mux 复用）。
// 方法不匹配的请求落到底部兜底模式，返回 405 + Allow。
func registerPointsRoutes(mux *http.ServeMux, s *Store, levels LevelConfig, secret []byte) {
	h := &api{store: s, levels: levels, secret: secret}
	mux.HandleFunc("POST /api/points", h.handleAddPoints)
	mux.HandleFunc("/api/points", h.methodNotAllowed(http.MethodPost))
	mux.HandleFunc("GET /api/pet/me/log", h.handleLog)
	mux.HandleFunc("/api/pet/me/log", h.methodNotAllowed(http.MethodGet))
}

type api struct {
	store  *Store
	levels LevelConfig
	secret []byte
}

// addPointsReq 是 POST /api/points 的请求体；requestId 非空时用作幂等键。
type addPointsReq struct {
	Reason    string `json:"reason"`
	Value     int    `json:"value"`
	RequestID string `json:"requestId"`
}

// petView 是响应中 pet 字段的形状；满级时 NextLevelPoints 序列化为 null。
type petView struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Species         string `json:"species"`
	Rarity          string `json:"rarity"`
	Level           int    `json:"level"`
	Points          int    `json:"points"`
	NextLevelPoints *int   `json:"nextLevelPoints"`
}

type addPointsResp struct {
	Pet     petView `json:"pet"`
	LevelUp bool    `json:"levelUp"`
	Level   int     `json:"level"`
	Added   bool    `json:"added"`
}

type logEntry struct {
	ID        int64  `json:"id"`
	Value     int    `json:"value"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
}

type logResp struct {
	Items    []logEntry `json:"items"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
	Total    int        `json:"total"`
}

// handleAddPoints 加分入账：事务内更新 pets.points/level 并追加流水；
// 同 pet 同 requestId 的重复提交不重复计分，返回当前状态（added=false）。
func (a *api) handleAddPoints(w http.ResponseWriter, r *http.Request) {
	studentID, ok := a.auth(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "需要有效的学生 token")
		return
	}
	var req addPointsReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if n := utf8.RuneCountInString(reason); n == 0 || n > maxReasonLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("理由需为 1..%d 个字符", maxReasonLen))
		return
	}
	if req.Value < minPointsValue || req.Value > maxPointsValue {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("分值需在 %d..%d 之间", minPointsValue, maxPointsValue))
		return
	}

	var resp addPointsResp
	notFound := false
	err := a.store.tx(r.Context(), func(tx *txc) error {
		pet, err := tx.petByStudent(studentID)
		if err != nil {
			if err == sqlErrNoRows {
				notFound = true
			}
			return err
		}
		if req.RequestID != "" {
			exists, err := tx.logExists(pet.id, req.RequestID)
			if err != nil {
				return err
			}
			if exists {
				resp = addPointsResp{Pet: pet.view(a.levels), Level: pet.level, Added: false}
				return nil
			}
		}
		newPoints := pet.points + req.Value
		newLevel := LevelFor(newPoints, a.levels)
		if _, err := tx.exec(
			`UPDATE pets SET points = ?, level = ? WHERE id = ?`,
			newPoints, newLevel, pet.id,
		); err != nil {
			return err
		}
		if _, err := tx.exec(
			`INSERT INTO point_logs (pet_id, value, reason, request_id) VALUES (?, ?, ?, ?)`,
			pet.id, req.Value, reason, nullIfEmpty(req.RequestID),
		); err != nil {
			return err
		}
		resp = addPointsResp{
			Pet:     pet.withGrowth(newLevel, newPoints).view(a.levels),
			LevelUp: newLevel > pet.level,
			Level:   newLevel,
			Added:   true,
		}
		return nil
	})
	if err != nil {
		if notFound {
			writeError(w, http.StatusNotFound, "该学生还没有宠物")
			return
		}
		writeError(w, http.StatusInternalServerError, "加分入账失败")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLog 返回当前学生宠物的积分流水（按 id 倒序分页，pageSize 固定 20）。
func (a *api) handleLog(w http.ResponseWriter, r *http.Request) {
	studentID, ok := a.auth(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "需要有效的学生 token")
		return
	}
	page, err := parsePage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "page 参数非法")
		return
	}

	var resp logResp
	notFound := false
	err = a.store.tx(r.Context(), func(tx *txc) error {
		pet, err := tx.petByStudent(studentID)
		if err != nil {
			if err == sqlErrNoRows {
				notFound = true
			}
			return err
		}
		total, err := tx.countLogs(pet.id)
		if err != nil {
			return err
		}
		items, err := tx.listLogs(pet.id, page)
		if err != nil {
			return err
		}
		resp = logResp{Items: items, Page: page, PageSize: logPageSize, Total: total}
		return nil
	})
	if err != nil {
		if notFound {
			writeError(w, http.StatusNotFound, "该学生还没有宠物")
			return
		}
		writeError(w, http.StatusInternalServerError, "读取流水失败")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// auth 从 Authorization: Bearer 头解析并校验 token，返回 studentID。
func (a *api) auth(r *http.Request) (int64, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return 0, false
	}
	id, err := VerifyToken(a.secret, strings.TrimPrefix(h, prefix))
	return id, err == nil
}

// methodNotAllowed 返回固定允许方法的 405 处理器。
func (a *api) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
	}
}

// maxPage 是 page 的上界：超出后 (page-1)*logPageSize 会整型溢出。
const maxPage = math.MaxInt / logPageSize

// parsePage 解析 page 查询参数：缺省 1，非数字、<1 或超出上界报错。
func parsePage(r *http.Request) (int, error) {
	p := r.URL.Query().Get("page")
	if p == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > maxPage {
		return 0, fmt.Errorf("page 非法: %q", p)
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
