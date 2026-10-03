package server

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 单次加分的分值上下限与理由长度上限（M2）。
const (
	minPointsValue = 1
	maxPointsValue = 10
	maxReasonLen   = 100
)

// logPageSize 是积分流水的固定分页大小（M2）。
const logPageSize = 20

// maxPage 是 page 上界：超出后 (page-1)*logPageSize 会整型溢出。
const maxPage = math.MaxInt / logPageSize

type addPointsRequest struct {
	Reason    string `json:"reason"`
	Value     int    `json:"value"`
	RequestID string `json:"requestId"`
}

// handleAddPoints 加分入账（M2）：事务内更新 pets.points/level 并追加流水；
// 同 pet 同 requestId 的重复提交不重复计分，幂等重放返回当前档案（added=false）。
func (s *srv) handleAddPoints(w http.ResponseWriter, r *http.Request) {
	studentID := r.Context().Value(ctxKeyStudent).(int64)

	var req addPointsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if n := utf8.RuneCountInString(reason); n == 0 || n > maxReasonLen {
		writeJSON(w, http.StatusBadRequest, errJSON("理由需为 1..%d 个字符", maxReasonLen))
		return
	}
	if req.Value < minPointsValue || req.Value > maxPointsValue {
		writeJSON(w, http.StatusBadRequest, errJSON("分值需在 %d..%d 之间", minPointsValue, maxPointsValue))
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		writeInternal(w, err, "db begin")
		return
	}
	defer tx.Rollback()

	var (
		petID, level, points int64
		speciesID, petName   string
	)
	err = tx.QueryRow(
		`SELECT id, species_id, name, level, points FROM pets WHERE student_id = ?`,
		studentID,
	).Scan(&petID, &speciesID, &petName, &level, &points)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("尚未领养宠物"))
		return
	}
	if err != nil {
		writeInternal(w, err, "load pet")
		return
	}
	sp, ok := speciesByID[speciesID]
	if !ok {
		writeInternal(w, errors.New("species not found: "+speciesID), "load pet")
		return
	}

	if req.RequestID != "" {
		var exists int
		err := tx.QueryRow(
			`SELECT 1 FROM point_logs WHERE pet_id = ? AND request_id = ? LIMIT 1`,
			petID, req.RequestID,
		).Scan(&exists)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeInternal(w, err, "idempotency check")
			return
		}
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"pet":     petJSON(petID, petName, int(level), int(points), sp, s.levels),
				"levelUp": false,
				"level":   int(level),
				"added":   false,
			})
			return
		}
	}

	newPoints := int(points) + req.Value
	newLevel := LevelFor(newPoints, s.levels)
	if _, err := tx.Exec(`UPDATE pets SET points = ?, level = ? WHERE id = ?`, newPoints, newLevel, petID); err != nil {
		writeInternal(w, err, "update pet")
		return
	}
	// request_id 空串存 NULL：部分唯一索引不约束 NULL，无 requestId 的加分互不冲突；
	// operator=student 标记学生自助加分（教师端点写 teacher，供 M3 班级墙/审计）
	if _, err := tx.Exec(
		`INSERT INTO point_logs (pet_id, delta, reason, request_id, operator) VALUES (?, ?, ?, ?, 'student')`,
		petID, req.Value, reason, nilIfEmpty(req.RequestID),
	); err != nil {
		writeInternal(w, err, "insert point log")
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternal(w, err, "commit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pet":     petJSON(petID, petName, newLevel, newPoints, sp, s.levels),
		"levelUp": newLevel > int(level),
		"level":   newLevel,
		"added":   true,
	})
}

// logEntry 是积分流水中单条记录的响应形状（Value 对应库中 delta，
// Operator 区分学生自助与教师代加，M4）。
type logEntry struct {
	ID        int64  `json:"id"`
	Value     int    `json:"value"`
	Reason    string `json:"reason"`
	Operator  string `json:"operator"`
	CreatedAt string `json:"createdAt"`
}

// handleLog 返回当前学生宠物的积分流水（按 id 倒序分页，pageSize 固定 20）。
func (s *srv) handleLog(w http.ResponseWriter, r *http.Request) {
	studentID := r.Context().Value(ctxKeyStudent).(int64)

	page, err := parsePage(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON("page 参数非法"))
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	var petID int64
	err = s.db.QueryRow(`SELECT id FROM pets WHERE student_id = ?`, studentID).Scan(&petID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("尚未领养宠物"))
		return
	}
	if err != nil {
		writeInternal(w, err, "load pet")
		return
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM point_logs WHERE pet_id = ?`, petID).Scan(&total); err != nil {
		writeInternal(w, err, "count logs")
		return
	}
	rows, err := s.db.Query(
		`SELECT id, delta, reason, operator, created_at FROM point_logs WHERE pet_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`,
		petID, logPageSize, (page-1)*logPageSize,
	)
	if err != nil {
		writeInternal(w, err, "list logs")
		return
	}
	defer rows.Close()

	items := make([]logEntry, 0, logPageSize)
	for rows.Next() {
		var e logEntry
		if err := rows.Scan(&e.ID, &e.Value, &e.Reason, &e.Operator, &e.CreatedAt); err != nil {
			writeInternal(w, err, "scan log")
			return
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		writeInternal(w, err, "list logs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"page":     page,
		"pageSize": logPageSize,
		"total":    total,
	})
}

// parsePage 解析 page 查询参数：缺省 1，非数字、<1 或超出上界（防整型溢出）报错。
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
