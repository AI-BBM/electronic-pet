package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
)

// writeJSON 统一 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// maxBodyBytes M1 各 POST 的合法请求体远小于该上限，防无界解码 DoS。
const maxBodyBytes = 64 << 10

var errBodyTooLarge = errors.New("request body too large")

// decodeJSON 严格解析请求体；超限返回 errBodyTooLarge（413）。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errBodyTooLarge
		}
		return errors.New("invalid JSON body")
	}
	if dec.More() { // 拒绝单个 JSON 对象之后的尾随内容
		return errors.New("invalid JSON body")
	}
	return nil
}

// writeInternal 记录内部错误到服务端日志，向客户端只回笼统文案（不透传驱动细节）。
func writeInternal(w http.ResponseWriter, err error, context string) {
	log.Printf("[500] %s: %v", context, err)
	writeJSON(w, http.StatusInternalServerError, errJSON("服务器开小差了，请稍后再试"))
}

type joinRequest struct {
	ClassCode string `json:"classCode"`
	Name      string `json:"name"`
	StudentNo string `json:"studentNo"`
}

// handleJoin 班级码 + 姓名 + 学号进入；同班同学号幂等，签发新 token。
func (s *srv) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	req.ClassCode = strings.TrimSpace(req.ClassCode)
	req.Name = strings.TrimSpace(req.Name)
	req.StudentNo = strings.TrimSpace(req.StudentNo)
	if req.ClassCode == "" || req.Name == "" || req.StudentNo == "" {
		writeJSON(w, http.StatusBadRequest, errJSON("classCode、name、studentNo 均不能为空"))
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

	if _, err := tx.Exec(
		`INSERT INTO classes(code, teacher_passcode) VALUES(?, ?) ON CONFLICT(code) DO NOTHING`,
		req.ClassCode, generatePasscode(),
	); err != nil {
		writeInternal(w, err, "upsert class")
		return
	}
	var classID int64
	if err := tx.QueryRow(`SELECT id FROM classes WHERE code = ?`, req.ClassCode).Scan(&classID); err != nil {
		writeInternal(w, err, "load class")
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO students(class_id, name, student_no) VALUES(?,?,?)
		 ON CONFLICT(class_id, student_no) DO NOTHING`, classID, req.Name, req.StudentNo); err != nil {
		writeInternal(w, err, "upsert student")
		return
	}
	var studentID int64
	if err := tx.QueryRow(
		`SELECT id FROM students WHERE class_id = ? AND student_no = ?`, classID, req.StudentNo,
	).Scan(&studentID); err != nil {
		writeInternal(w, err, "load student")
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternal(w, err, "commit")
		return
	}

	pet, err := s.petByStudentID(studentID)
	if err != nil {
		writeInternal(w, err, "load pet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": signToken(s.tokenSecret, studentID),
		"pet":   pet, // 无宠物时为 null
	})
}

// petByStudentID 查学生宠物档案；无宠物返回 nil 不视为错误。
func (s *srv) petByStudentID(studentID int64) (map[string]any, error) {
	var (
		petID, level, points int64
		speciesID, petName   string
		nameCustomized       int64
	)
	err := s.db.QueryRow(
		`SELECT id, species_id, name, name_customized, level, points FROM pets WHERE student_id = ?`,
		studentID,
	).Scan(&petID, &speciesID, &petName, &nameCustomized, &level, &points)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sp, ok := speciesByID[speciesID]
	if !ok {
		return nil, errors.New("species not found: " + speciesID)
	}
	return petJSON(petID, petName, int(level), int(points), sp, s.levels), nil
}

// handleEggs 蛋架列表（6 颗，颜色互异，仅视觉）。
func (s *srv) handleEggs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"eggs": eggList})
}

type adoptRequest struct {
	EggID string `json:"eggId"`
}

// handleAdopt 领蛋孵化：事务内绑定学生与随机种类；一学生一宠。
func (s *srv) handleAdopt(w http.ResponseWriter, r *http.Request) {
	var req adoptRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	eggID := strings.TrimSpace(req.EggID)
	if eggID != "random" && !validEgg(eggID) {
		writeJSON(w, http.StatusBadRequest, errJSON("无效的 eggId"))
		return
	}
	studentID := r.Context().Value(ctxKeyStudent).(int64)

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		writeInternal(w, err, "db begin")
		return
	}
	defer tx.Rollback()

	var petID int64
	err = tx.QueryRow(`SELECT id FROM pets WHERE student_id = ?`, studentID).Scan(&petID)
	if err == nil {
		writeJSON(w, http.StatusConflict, errJSON("该学生已认领宠物"))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeInternal(w, err, "load pet")
		return
	}

	sp := pickSpecies()
	res, err := tx.Exec(
		`INSERT INTO pets(student_id, species_id, name, level, points, egg_id) VALUES(?,?,?,?,?,?)`,
		studentID, sp.ID, sp.Name, 1, 0, eggID,
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

// handlePetMe 宠物档案 + 积分流水摘要（M1 无加分，恒为空数组）。
func (s *srv) handlePetMe(w http.ResponseWriter, r *http.Request) {
	studentID := r.Context().Value(ctxKeyStudent).(int64)
	pet, err := s.petByStudentID(studentID)
	if err != nil {
		writeInternal(w, err, "load pet")
		return
	}
	if pet == nil {
		writeJSON(w, http.StatusNotFound, errJSON("尚未领养宠物"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pet":        pet,
		"logSummary": []any{},
	})
}

type renameRequest struct {
	Name string `json:"name"`
}

// handleRename 宠物名自定义，一生一次（默认名为种类名）。
func (s *srv) handleRename(w http.ResponseWriter, r *http.Request) {
	var req renameRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errJSON("name 不能为空"))
		return
	}
	if len([]rune(name)) > 24 {
		writeJSON(w, http.StatusBadRequest, errJSON("name 不能超过 24 个字符"))
		return
	}
	studentID := r.Context().Value(ctxKeyStudent).(int64)

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		writeInternal(w, err, "db begin")
		return
	}
	defer tx.Rollback()

	var customized int64
	var petID int64
	err = tx.QueryRow(`SELECT id, name_customized FROM pets WHERE student_id = ?`, studentID).Scan(&petID, &customized)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("尚未领养宠物"))
		return
	}
	if err != nil {
		writeInternal(w, err, "load pet")
		return
	}
	if customized != 0 {
		writeJSON(w, http.StatusConflict, errJSON("宠物名已自定义过，不可再改"))
		return
	}
	if _, err := tx.Exec(`UPDATE pets SET name = ?, name_customized = 1 WHERE id = ?`, name, petID); err != nil {
		writeInternal(w, err, "rename")
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternal(w, err, "commit")
		return
	}
	pet, err := s.petByStudentID(studentID)
	if err != nil {
		writeInternal(w, err, "load pet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pet": pet})
}
