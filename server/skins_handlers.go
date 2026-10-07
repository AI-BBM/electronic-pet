package server

// M12 皮肤商店（#51，教师代购代切换）。皮肤 = 场景主题，按阶段定价，
// 复用 #49 的 scenes/{scene_id}.jpg 资产；购买永久解锁，切换即时生效。

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// loadClassPet 校验 studentID 属本班在册学生并加载其宠物（皮肤三接口共用前置）。
// 学生不存在 404、无宠物 404（响应已写好）；布尔返回 false 时调用方直接返回。
func (s *srv) loadClassPet(w http.ResponseWriter, classID, studentID int64) (
	petID int64, level, currency int, activeScene string, ok bool,
) {
	var live int
	err := s.db.QueryRow(
		`SELECT 1 FROM students WHERE id = ? AND class_id = ? AND deleted_at IS NULL`,
		studentID, classID,
	).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("学生不存在"))
		return 0, 0, 0, "", false
	}
	if err != nil {
		writeInternal(w, err, "load student")
		return 0, 0, 0, "", false
	}
	var lvl, cur int64
	err = s.db.QueryRow(
		`SELECT id, level, currency, COALESCE(active_scene, '') FROM pets WHERE student_id = ?`,
		studentID,
	).Scan(&petID, &lvl, &cur, &activeScene)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("该学生还没有宠物"))
		return 0, 0, 0, "", false
	}
	if err != nil {
		writeInternal(w, err, "load pet")
		return 0, 0, 0, "", false
	}
	return petID, int(lvl), int(cur), activeScene, true
}

// skinEntry 皮肤商店目录行（打卡照款；owned/active 由宠物当前态标注）。
type skinEntry struct {
	SkinID      string `json:"skinId"`
	Name        string `json:"name"`
	Price       int    `json:"price"`
	ImageURL    string `json:"imageUrl"`
	Owned       bool   `json:"owned"`
	Active      bool   `json:"active"`
	Purchasable bool   `json:"purchasable"`
}

// handleTeacherSkinCatalog 皮肤目录：该阶段档位 + 积分余额 + 拥有/使用中标注。
func (s *srv) handleTeacherSkinCatalog(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)
	studentID, ok := pathID(w, r, "studentID")
	if !ok {
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	petID, level, currency, activeScene, ok := s.loadClassPet(w, classID, studentID)
	if !ok {
		return
	}
	speciesID := ""
	if err := s.db.QueryRow(
		`SELECT species_id FROM pets WHERE id = ?`, petID,
	).Scan(&speciesID); err != nil {
		writeInternal(w, err, "load species")
		return
	}
	stage := stageForLevel(level)

	owned := map[string]bool{}
	rows, err := s.db.Query(`SELECT scene_id FROM pet_skins WHERE pet_id = ?`, petID)
	if err != nil {
		writeInternal(w, err, "list owned skins")
		return
	}
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			writeInternal(w, err, "scan owned skins")
			return
		}
		owned[sid] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeInternal(w, err, "rows owned skins")
		return
	}

	entries := make([]skinEntry, 0, len(skinScenes))
	for _, sc := range skinScenes {
		entries = append(entries, skinEntry{
			SkinID:      sc.ID,
			Name:        sc.Name,
			Price:       SkinPrice,
			ImageURL:    skinImageURL(speciesID, stage, sc.ID),
			Owned:       owned[sc.ID],
			Active:      activeScene == sc.ID,
			Purchasable: !owned[sc.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"currency":    currency,
		"stage":       stage,
		"activeScene": nilIfEmpty(activeScene),
		"skins":       entries,
	})
}

type skinBuyRequest struct {
	SkinID    string `json:"skinId"`
	RequestID string `json:"requestId"`
}

// handleTeacherSkinBuy 购买皮肤：阶段匹配（400）、未拥有（409）、余额充足
// （409，扣减带条件双花兜底）；事务内扣积分+解锁+spend 流水；
// requestId 幂等重放返回当前态不二次扣减。
func (s *srv) handleTeacherSkinBuy(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)
	studentID, ok := pathID(w, r, "studentID")
	if !ok {
		return
	}
	var req skinBuyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	sc, ok := skinByScene(strings.TrimSpace(req.SkinID))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errJSON("skinId 不合法"))
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	petID, _, currency, activeScene, ok := s.loadClassPet(w, classID, studentID)
	if !ok {
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		writeInternal(w, err, "db begin")
		return
	}
	defer tx.Rollback()

	// 幂等：同 pet 同 request_id 的 spend 流水重放返回当前态（不二次扣减）。
	if req.RequestID != "" {
		var exists int
		err := tx.QueryRow(
			`SELECT 1 FROM point_logs WHERE pet_id = ? AND request_id = ? AND type = 'spend' LIMIT 1`,
			petID, req.RequestID,
		).Scan(&exists)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeInternal(w, err, "buy idempotency check")
			return
		}
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"purchased": false, "currency": currency,
				"activeScene": nilIfEmpty(activeScene),
			})
			return
		}
	}

	// 已拥有 409（幂等查重之后判定，重放不受影响）。
	var owned int
	err = tx.QueryRow(
		`SELECT 1 FROM pet_skins WHERE pet_id = ? AND scene_id = ?`, petID, sc.ID,
	).Scan(&owned)
	if err == nil {
		writeJSON(w, http.StatusConflict, errJSON("该皮肤已拥有"))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeInternal(w, err, "check owned")
		return
	}
	if currency < SkinPrice {
		writeJSON(w, http.StatusConflict, errJSON("积分余额不足：需 %d，现有 %d", SkinPrice, currency))
		return
	}

	// 扣减带余额条件（并发双花兜底），再解锁 + spend 流水 + 设为当前展示。
	res, err := tx.Exec(
		`UPDATE pets SET currency = currency - ? WHERE id = ? AND currency >= ?`,
		SkinPrice, petID, SkinPrice,
	)
	if err != nil {
		writeInternal(w, err, "spend currency")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusConflict, errJSON("积分余额不足：需 %d，现有 %d", SkinPrice, currency))
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO pet_skins (pet_id, scene_id) VALUES (?, ?)`, petID, sc.ID,
	); err != nil {
		if isUniqueConstraintErr(err) {
			writeJSON(w, http.StatusConflict, errJSON("该皮肤已拥有"))
			return
		}
		writeInternal(w, err, "insert pet skin")
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO point_logs (pet_id, delta, reason, request_id, operator, type) VALUES (?, ?, ?, ?, 'teacher', 'spend')`,
		petID, -SkinPrice, "购买皮肤："+sc.Name, nilIfEmpty(req.RequestID),
	); err != nil {
		writeInternal(w, err, "insert spend log")
		return
	}
	if _, err := tx.Exec(`UPDATE pets SET active_scene = ? WHERE id = ?`, sc.ID, petID); err != nil {
		writeInternal(w, err, "activate skin")
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternal(w, err, "commit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"purchased":   true,
		"currency":    currency - SkinPrice,
		"activeScene": sc.ID,
	})
}

type skinActivateRequest struct {
	SkinID string `json:"skinId"` // 空串 = 切回默认无皮肤态
}

// handleTeacherSkinActivate 切换展示皮肤：未解锁 403；空 sceneId 切回默认。
func (s *srv) handleTeacherSkinActivate(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)
	studentID, ok := pathID(w, r, "studentID")
	if !ok {
		return
	}
	var req skinActivateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	sceneID := strings.TrimSpace(req.SkinID)
	if sceneID != "" {
		if _, ok := skinByScene(sceneID); !ok {
			writeJSON(w, http.StatusBadRequest, errJSON("skinId 不合法"))
			return
		}
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	petID, _, _, _, ok := s.loadClassPet(w, classID, studentID)
	if !ok {
		return
	}
	if sceneID != "" {
		var unlocked int
		err := s.db.QueryRow(
			`SELECT 1 FROM pet_skins WHERE pet_id = ? AND scene_id = ?`, petID, sceneID,
		).Scan(&unlocked)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusForbidden, errJSON("该皮肤未解锁，请先购买"))
			return
		}
		if err != nil {
			writeInternal(w, err, "check unlocked")
			return
		}
	}
	if _, err := s.db.Exec(
		`UPDATE pets SET active_scene = NULLIF(?, '') WHERE id = ?`, sceneID, petID,
	); err != nil {
		writeInternal(w, err, "activate skin")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activeScene": nilIfEmpty(sceneID)})
}
