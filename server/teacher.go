package server

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

// 教师端常量（M4）：加分额度高于学生自助（1..10），理由长度与学生端一致。
const (
	teacherMinPointsValue = 1
	teacherMaxPointsValue = 50
	maxPasscodeLen        = 32
)

// generatePasscode 生成 16 位随机十六进制教师口令（建班生成、迁移回填共用）。
func generatePasscode() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

type teacherLoginRequest struct {
	ClassCode       string `json:"classCode"`
	TeacherPasscode string `json:"teacherPasscode"`
}

// handleTeacherLogin 班级码 + 教师密码 → 教师 token（role=teacher，id=classID）。
// 登录口免鉴权；密码比较用常数时间。
func (s *srv) handleTeacherLogin(w http.ResponseWriter, r *http.Request) {
	var req teacherLoginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	req.ClassCode = strings.TrimSpace(req.ClassCode)
	req.TeacherPasscode = strings.TrimSpace(req.TeacherPasscode)
	if req.ClassCode == "" || req.TeacherPasscode == "" {
		writeJSON(w, http.StatusBadRequest, errJSON("classCode、teacherPasscode 均不能为空"))
		return
	}

	s.dbMu.Lock()
	var (
		classID  int64
		passcode sql.NullString
	)
	err := s.db.QueryRow(
		`SELECT id, teacher_passcode FROM classes WHERE code = ?`, req.ClassCode,
	).Scan(&classID, &passcode)
	s.dbMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("班级不存在"))
		return
	}
	if err != nil {
		writeInternal(w, err, "load class")
		return
	}
	if subtle.ConstantTimeCompare([]byte(passcode.String), []byte(req.TeacherPasscode)) != 1 {
		writeJSON(w, http.StatusUnauthorized, errJSON("教师密码不正确"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": signTeacherToken(s.tokenSecret, classID),
	})
}

// teacherStudentID 解析教师 token 所属班级内指定学号的学生 id；不存在返回 0。
func teacherStudentID(tx *sql.Tx, classID int64, studentNo string) (int64, error) {
	var id int64
	err := tx.QueryRow(
		`SELECT id FROM students WHERE class_id = ? AND student_no = ?`, classID, studentNo,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

type teacherAdoptRequest struct {
	StudentNo string `json:"studentNo"`
}

// handleTeacherAdopt 代学生领蛋孵化：随机定种类（M1 规则），已领养 409。
func (s *srv) handleTeacherAdopt(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)

	var req teacherAdoptRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	studentNo := strings.TrimSpace(req.StudentNo)
	if studentNo == "" {
		writeJSON(w, http.StatusBadRequest, errJSON("studentNo 不能为空"))
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

	studentID, err := teacherStudentID(tx, classID, studentNo)
	if err != nil {
		writeInternal(w, err, "load student")
		return
	}
	if studentID == 0 {
		writeJSON(w, http.StatusNotFound, errJSON("本班不存在该学号"))
		return
	}

	var petID int64
	err = tx.QueryRow(`SELECT id FROM pets WHERE student_id = ?`, studentID).Scan(&petID)
	if err == nil {
		writeJSON(w, http.StatusConflict, errJSON("该学生已领养宠物"))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeInternal(w, err, "load pet")
		return
	}

	sp := pickSpecies()
	res, err := tx.Exec(
		`INSERT INTO pets(student_id, species_id, name, level, points, egg_id) VALUES(?,?,?,?,?,?)`,
		studentID, sp.ID, sp.Name, 1, 0, "teacher",
	)
	if err != nil {
		writeInternal(w, err, "insert pet")
		return
	}
	petID, err = res.LastInsertId()
	if err != nil {
		writeInternal(w, err, "pet id")
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternal(w, err, "commit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pet": petJSON(petID, sp.Name, 1, 0, sp, s.levels),
	})
}

type teacherPointsRequest struct {
	StudentNo string `json:"studentNo"`
	Reason    string `json:"reason"`
	Value     int    `json:"value"`
	RequestID string `json:"requestId"`
}

// handleTeacherPoints 教师给学生加分：1..50、流水 operator=teacher、跨级即时生效。
func (s *srv) handleTeacherPoints(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)

	var req teacherPointsRequest
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
	if req.Value < teacherMinPointsValue || req.Value > teacherMaxPointsValue {
		writeJSON(w, http.StatusBadRequest, errJSON("分值需在 %d..%d 之间", teacherMinPointsValue, teacherMaxPointsValue))
		return
	}
	studentNo := strings.TrimSpace(req.StudentNo)
	if studentNo == "" {
		writeJSON(w, http.StatusBadRequest, errJSON("studentNo 不能为空"))
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

	studentID, err := teacherStudentID(tx, classID, studentNo)
	if err != nil {
		writeInternal(w, err, "load student")
		return
	}
	if studentID == 0 {
		writeJSON(w, http.StatusNotFound, errJSON("本班不存在该学号"))
		return
	}

	var (
		petID, level, points int64
		speciesID, petName   string
	)
	err = tx.QueryRow(
		`SELECT id, species_id, name, level, points FROM pets WHERE student_id = ?`,
		studentID,
	).Scan(&petID, &speciesID, &petName, &level, &points)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("该学生还没有宠物"))
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
	if _, err := tx.Exec(
		`INSERT INTO point_logs (pet_id, delta, reason, request_id, operator) VALUES (?, ?, ?, ?, 'teacher')`,
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

// rosterEntry 是花名册单行（未领养学生 species/level/points 为零值）。
type rosterEntry struct {
	StudentNo    string `json:"studentNo"`
	Name         string `json:"name"`
	Adopted      bool   `json:"adopted"`
	SpeciesID    string `json:"speciesId"`
	SpeciesName  string `json:"speciesName"`
	Level        int    `json:"level"`
	Points       int    `json:"points"`
	LastPointsAt string `json:"lastPointsAt"`
}

// handleTeacherRoster 全班花名册（只读）：姓名/学号、宠物种类/等级、积分、最近加分时间。
func (s *srv) handleTeacherRoster(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	rows, err := s.db.Query(
		`SELECT s.student_no, s.name, p.id, p.species_id, p.level, p.points,
		        (SELECT MAX(created_at) FROM point_logs pl WHERE pl.pet_id = p.id)
		 FROM students s LEFT JOIN pets p ON p.student_id = s.id
		 WHERE s.class_id = ?
		 ORDER BY s.student_no ASC`,
		classID,
	)
	if err != nil {
		writeInternal(w, err, "list roster")
		return
	}
	defer rows.Close()

	students := make([]rosterEntry, 0)
	for rows.Next() {
		var (
			e             rosterEntry
			petID         sql.NullInt64
			speciesID     sql.NullString
			level, points sql.NullInt64
			lastPoints    sql.NullString
		)
		if err := rows.Scan(&e.StudentNo, &e.Name, &petID, &speciesID, &level, &points, &lastPoints); err != nil {
			writeInternal(w, err, "scan roster")
			return
		}
		if petID.Valid {
			e.Adopted = true
			e.Level = int(level.Int64)
			e.Points = int(points.Int64)
			e.LastPointsAt = lastPoints.String
			if sp, ok := speciesByID[speciesID.String]; ok {
				e.SpeciesID = sp.ID
				e.SpeciesName = sp.Name
			}
		}
		students = append(students, e)
	}
	if err := rows.Err(); err != nil {
		writeInternal(w, err, "list roster")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"students": students})
}

type teacherPasscodeRequest struct {
	OldPasscode string `json:"oldPasscode"`
	NewPasscode string `json:"newPasscode"`
}

// handleTeacherPasscode 修改本班教师密码：旧密码常数时间比对，新密码 1..32 字符。
func (s *srv) handleTeacherPasscode(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)

	var req teacherPasscodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	newPasscode := strings.TrimSpace(req.NewPasscode)
	if n := utf8.RuneCountInString(newPasscode); n == 0 || n > maxPasscodeLen {
		writeJSON(w, http.StatusBadRequest, errJSON("新密码需为 1..%d 个字符", maxPasscodeLen))
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	var current sql.NullString
	err := s.db.QueryRow(`SELECT teacher_passcode FROM classes WHERE id = ?`, classID).Scan(&current)
	if err != nil {
		writeInternal(w, err, "load passcode")
		return
	}
	if subtle.ConstantTimeCompare([]byte(current.String), []byte(strings.TrimSpace(req.OldPasscode))) != 1 {
		writeJSON(w, http.StatusForbidden, errJSON("旧密码不正确"))
		return
	}
	if _, err := s.db.Exec(`UPDATE classes SET teacher_passcode = ? WHERE id = ?`, newPasscode, classID); err != nil {
		writeInternal(w, err, "update passcode")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
