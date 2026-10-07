package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	crand "crypto/rand"
	"math/big"

	"golang.org/x/crypto/bcrypt"
)

// 教师端常量：代加分额度 1..50（M4 契约延续），理由长度与学生端一致。
const (
	teacherMinPointsValue = 1
	teacherMaxPointsValue = 50
	maxReasonLen          = 100
	maxPasswordLen        = 72 // bcrypt 输入上限
	maxEmailLen           = 254
	minPasswordLen        = 8  // PRD M6：密码 ≥8 位
	trashRetentionDays    = 90 // 垃圾桶保留 3 个月
)

// trashEntry 是垃圾桶单行（deletedAt 为格式化时间串）。
// ImageURL/Silhouette：有宠为该宠当前阶段图/剪影直链，无宠为空串（#32 W1）。
type trashEntry struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	StudentNo  string `json:"studentNo"`
	DeletedAt  string `json:"deletedAt"`
	ImageURL   string `json:"imageUrl"`
	Silhouette string `json:"silhouette"`
}

type studentEntry struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	StudentNo string `json:"studentNo"`
}

// ---------- 邮箱验证码 ----------

type emailCodeRequest struct {
	Email string `json:"email"`
}

func validEmail(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxEmailLen {
		return false
	}
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n")
}

// handleTeacherEmailCode 发送注册验证码（SMTP 未配置时 mock 记日志）。
func (s *srv) handleTeacherEmailCode(w http.ResponseWriter, r *http.Request) {
	var req emailCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	// 统一小写规范化（与注册/登录一致，防大小写变体绕过限速/唯一键）
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !validEmail(email) {
		writeJSON(w, http.StatusBadRequest, errJSON("email 格式不合法"))
		return
	}
	if err := s.sendEmailCode(email); err != nil {
		if errors.Is(err, errCodeRateLimited) || errors.Is(err, errCodeDailyMax) {
			writeJSON(w, http.StatusTooManyRequests, errJSON("%s", err.Error()))
			return
		}
		writeInternal(w, err, "send email code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 注册与登录 ----------

type teacherRegisterRequest struct {
	Email     string `json:"email"`
	Code      string `json:"code"`
	Password  string `json:"password"`
	ClassName string `json:"className"`
}

// handleTeacherRegister 邮箱+验证码+密码注册：注册即建班，一生一班。
func (s *srv) handleTeacherRegister(w http.ResponseWriter, r *http.Request) {
	var req teacherRegisterRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	// 邮箱统一小写规范化（唯一键 BINARY 排序，不做则大小写变体可注册多账号）
	email := strings.ToLower(strings.TrimSpace(req.Email))
	className := strings.TrimSpace(req.ClassName)
	if !validEmail(email) {
		writeJSON(w, http.StatusBadRequest, errJSON("email 格式不合法"))
		return
	}
	// bcrypt 输入上限 72 字节：按字节而非 rune 校验，超限前置 400（否则 500）
	if n := utf8.RuneCountInString(req.Password); n < minPasswordLen || len(req.Password) > maxPasswordLen {
		writeJSON(w, http.StatusBadRequest, errJSON("password 需为 %d..%d 位且不超过 %d 字节", minPasswordLen, maxPasswordLen, maxPasswordLen))
		return
	}
	if className == "" {
		writeJSON(w, http.StatusBadRequest, errJSON("className 不能为空"))
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	// 邮箱查重前置（同锁内单查询，无事务占用连接）：已注册 409 优先于码校验
	var emailTaken int
	if err := s.db.QueryRow(`SELECT 1 FROM teachers WHERE email = ?`, email).Scan(&emailTaken); err == nil {
		writeJSON(w, http.StatusConflict, errJSON("该邮箱已注册"))
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeInternal(w, err, "check email")
		return
	}

	// 验证码校验必须在注册事务之前：单连接池下事务持连接期间再用 s.db 会死锁；
	// fail 计数/删码也需跨事务持久。dbMu 持有期间操作等价原子，无并发重放窗口。
	if err := checkEmailCodeDB(s.db, email, strings.TrimSpace(req.Code)); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		writeInternal(w, err, "db begin")
		return
	}
	defer tx.Rollback()

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeInternal(w, err, "hash password")
		return
	}
	code, err := s.generateClassCode(tx)
	if err != nil {
		writeInternal(w, err, "gen class code")
		return
	}
	res, err := tx.Exec(`INSERT INTO classes(code) VALUES(?)`, code)
	if err != nil {
		writeInternal(w, err, "insert class")
		return
	}
	classID, err := res.LastInsertId()
	if err != nil {
		writeInternal(w, err, "class id")
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO teachers(class_id, email, pass_hash) VALUES(?,?,?)`,
		classID, email, string(hash),
	); err != nil {
		writeInternal(w, err, "insert teacher")
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternal(w, err, "commit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": signTeacherToken(s.tokenSecret, classID),
	})
}

// generateClassCode 生成 "C" + 6 位随机的唯一班级码。在调用方事务内查重
// （单连接池下 s.db 查询会与未提交事务争抢唯一连接而死锁）。
func (s *srv) generateClassCode(tx *sql.Tx) (string, error) {
	for i := 0; i < 32; i++ {
		code := "C" + randomDigits(6)
		var exists int
		err := tx.QueryRow(`SELECT 1 FROM classes WHERE code = ?`, code).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return code, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "C" + randomDigits(12), nil // 极端兜底：更长随机串
}

type teacherLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleTeacherLogin 邮箱 + 密码 → 教师 token（role=teacher，id=classID）。
// M6 起替代 M4 口令式登录。
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
	// 统一小写规范化（与注册一致）
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errJSON("email、password 均不能为空"))
		return
	}

	s.dbMu.Lock()
	var (
		classID  int64
		passHash string
	)
	err := s.db.QueryRow(
		`SELECT class_id, pass_hash FROM teachers WHERE email = ?`, email,
	).Scan(&classID, &passHash)
	s.dbMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		// 邮箱不存在也跑一次 dummy 比较：抹平与存在分支的耗时差，防计时枚举邮箱
		_ = bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0X8a0GcH0p1Vf0NE1pUqYCTaEmW"), []byte(req.Password))
		writeJSON(w, http.StatusUnauthorized, errJSON("邮箱或密码不正确"))
		return
	}
	if err != nil {
		writeInternal(w, err, "load teacher")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(passHash), []byte(req.Password)) != nil {
		writeJSON(w, http.StatusUnauthorized, errJSON("邮箱或密码不正确"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": signTeacherToken(s.tokenSecret, classID),
	})
}

// ---------- 名单管理 ----------

type createStudentRequest struct {
	Name      string `json:"name"`
	StudentNo string `json:"studentNo"`
}

func cleanStudentFields(name, studentNo string) (string, string, bool) {
	name = strings.TrimSpace(name)
	studentNo = strings.TrimSpace(studentNo)
	if name == "" || studentNo == "" {
		return "", "", false
	}
	if utf8.RuneCountInString(name) > 24 || utf8.RuneCountInString(studentNo) > 24 {
		return "", "", false
	}
	return name, studentNo, true
}

// handleCreateStudent 增：姓名 + 学号（班内在册唯一）。
func (s *srv) handleCreateStudent(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)

	var req createStudentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	name, studentNo, ok := cleanStudentFields(req.Name, req.StudentNo)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errJSON("name、studentNo 均不能为空且不超过 24 字符"))
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	res, err := s.db.Exec(
		`INSERT INTO students(class_id, name, student_no) VALUES(?,?,?)`,
		classID, name, studentNo,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			writeJSON(w, http.StatusConflict, errJSON("该学号已在册"))
			return
		}
		writeInternal(w, err, "insert student")
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		writeInternal(w, err, "student id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"student": studentEntry{ID: id, Name: name, StudentNo: studentNo},
	})
}

type patchStudentRequest struct {
	Name      *string `json:"name"`
	StudentNo *string `json:"studentNo"`
}

// handlePatchStudent 改：姓名、学号（改号后班内仍须在册唯一）。
func (s *srv) handlePatchStudent(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)
	studentID, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	var req patchStudentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errJSON("请求体过大"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errJSON("%s", err.Error()))
		return
	}
	if req.Name == nil && req.StudentNo == nil {
		writeJSON(w, http.StatusBadRequest, errJSON("name、studentNo 至少提供一项"))
		return
	}
	name := strings.TrimSpace(deref(req.Name))
	studentNo := strings.TrimSpace(deref(req.StudentNo))
	if (req.Name != nil && name == "") || (req.StudentNo != nil && studentNo == "") {
		writeJSON(w, http.StatusBadRequest, errJSON("name、studentNo 不能为空白"))
		return
	}
	if utf8.RuneCountInString(name) > 24 || utf8.RuneCountInString(studentNo) > 24 {
		writeJSON(w, http.StatusBadRequest, errJSON("name、studentNo 不能超过 24 字符"))
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	// 必须是本班在册学生（垃圾桶内的不可改）
	var exists int
	err := s.db.QueryRow(
		`SELECT 1 FROM students WHERE id = ? AND class_id = ? AND deleted_at IS NULL`,
		studentID, classID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("学生不存在"))
		return
	}
	if err != nil {
		writeInternal(w, err, "load student")
		return
	}

	// 双字段更新放同一事务：学号撞号 409 时姓名不得已改（原子性）
	tx, err := s.db.Begin()
	if err != nil {
		writeInternal(w, err, "db begin")
		return
	}
	defer tx.Rollback()

	if req.Name != nil {
		if _, err := tx.Exec(`UPDATE students SET name = ? WHERE id = ?`, name, studentID); err != nil {
			writeInternal(w, err, "update name")
			return
		}
	}
	if req.StudentNo != nil {
		if _, err := tx.Exec(
			`UPDATE students SET student_no = ? WHERE id = ?`, studentNo, studentID,
		); err != nil {
			if isUniqueConstraintErr(err) {
				writeJSON(w, http.StatusConflict, errJSON("该学号已被在册学生占用"))
				return
			}
			writeInternal(w, err, "update student_no")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeInternal(w, err, "commit")
		return
	}

	var e studentEntry
	if err := s.db.QueryRow(
		`SELECT id, name, student_no FROM students WHERE id = ?`, studentID,
	).Scan(&e.ID, &e.Name, &e.StudentNo); err != nil {
		writeInternal(w, err, "reload student")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"student": e})
}

// handleDeleteStudent 删：进垃圾桶（软删除），宠物流水保留、花名册不再显示。
func (s *srv) handleDeleteStudent(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)
	studentID, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE students SET deleted_at = ?
		 WHERE id = ? AND class_id = ? AND deleted_at IS NULL`,
		time.Now().Unix(), studentID, classID,
	)
	if err != nil {
		writeInternal(w, err, "soft delete")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errJSON("学生不存在"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTeacherTrash 垃圾桶列表（按删除时间倒序）。#32 W1：LEFT JOIN 宠物下发图片直链。
func (s *srv) handleTeacherTrash(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	rows, err := s.db.Query(
		`SELECT s.id, s.name, s.student_no, s.deleted_at, p.species_id, p.level
		 FROM students s LEFT JOIN pets p ON p.student_id = s.id
		 WHERE s.class_id = ? AND s.deleted_at IS NOT NULL
		 ORDER BY s.deleted_at DESC`, classID,
	)
	if err != nil {
		writeInternal(w, err, "list trash")
		return
	}
	defer rows.Close()

	items := []trashEntry{}
	for rows.Next() {
		var (
			e         trashEntry
			deletedAt int64
			speciesID sql.NullString
			level     sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &e.Name, &e.StudentNo, &deletedAt, &speciesID, &level); err != nil {
			writeInternal(w, err, "scan trash")
			return
		}
		e.DeletedAt = time.Unix(deletedAt, 0).Format("2006-01-02 15:04:05")
		if speciesID.Valid {
			if sp, ok := speciesByID[speciesID.String]; ok {
				e.ImageURL = speciesImageURL(sp, int(level.Int64))
				e.Silhouette = sp.Silhouette
			}
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		writeInternal(w, err, "list trash")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleRestoreStudent 恢复：学号未被在册学生占用即可（冲突 409）。
func (s *srv) handleRestoreStudent(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)
	studentID, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE students SET deleted_at = NULL
		 WHERE id = ? AND class_id = ? AND deleted_at IS NOT NULL`,
		studentID, classID,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			writeJSON(w, http.StatusConflict, errJSON("该学号已被在册学生占用，无法恢复"))
			return
		}
		writeInternal(w, err, "restore")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errJSON("垃圾桶中不存在该学生"))
		return
	}
	var e studentEntry
	if err := s.db.QueryRow(
		`SELECT id, name, student_no FROM students WHERE id = ?`, studentID,
	).Scan(&e.ID, &e.Name, &e.StudentNo); err != nil {
		writeInternal(w, err, "reload student")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"student": e})
}

// ---------- 发宠物 / 加分 / 花名册（M4 契约延续，对象为名单内在册学生） ----------

type teacherAdoptRequest struct {
	StudentNo string `json:"studentNo"`
	// SpeciesID 可选（#32 W2）：为空/缺省按稀有度加权随机；指定时必须为
	// canonical 物种 id 之一，否则 400（校验先于任何 DB 操作）。
	SpeciesID string `json:"speciesId"`
}

// handleTeacherAdopt 代学生领蛋孵化：带 speciesId 则指定物种，否则随机定种类（M1 规则），已领养 409。
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

	// #32 W2：指定物种校验先于 DB（非法值不得触发任何查询/落库）。
	speciesChosen := strings.TrimSpace(req.SpeciesID)
	sp := speciesInfo{}
	if speciesChosen != "" {
		chosen, ok := speciesByID[speciesChosen]
		if !ok {
			writeJSON(w, http.StatusBadRequest, errJSON("speciesId 不合法，必须是支持的物种之一"))
			return
		}
		sp = chosen
	} else {
		sp = pickSpecies()
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		writeInternal(w, err, "db begin")
		return
	}
	defer tx.Rollback()

	studentID, err := liveStudentID(tx, classID, studentNo)
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

	studentID, err := liveStudentID(tx, classID, studentNo)
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
	// M12 双轨制：加分按分值 1:1 同步入账积分（currency），等级逻辑不动。
	if _, err := tx.Exec(
		`UPDATE pets SET points = ?, level = ?, currency = currency + ? WHERE id = ?`,
		newPoints, newLevel, req.Value, petID,
	); err != nil {
		writeInternal(w, err, "update pet")
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO point_logs (pet_id, delta, reason, request_id, operator, type) VALUES (?, ?, ?, ?, 'teacher', 'earn')`,
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
	// ID 学生数字 id（#35 D1：卡片/详情的改名、删除等 id 型操作的数据源）。
	ID           int64  `json:"id"`
	StudentNo    string `json:"studentNo"`
	Name         string `json:"name"`
	Adopted      bool   `json:"adopted"`
	SpeciesID    string `json:"speciesId"`
	SpeciesName  string `json:"speciesName"`
	PetName      string `json:"petName"`
	Level        int    `json:"level"`
	Points       int    `json:"points"`
	LastPointsAt string `json:"lastPointsAt"`
	// M12：积分余额与当前展示皮肤（空 = 默认无皮肤态）。
	Currency    int    `json:"currency"`
	ActiveScene string `json:"activeScene"`
	// #32 W1：已领养为当前阶段图/剪影直链；未领养为空串。
	ImageURL   string `json:"imageUrl"`
	Silhouette string `json:"silhouette"`
}

// handleTeacherRoster 花名册（只读，仅在册学生）：姓名/学号、宠物（含宠物名）、积分、最近加分。
func (s *srv) handleTeacherRoster(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	rows, err := s.db.Query(
		`SELECT s.id, s.student_no, s.name, p.id, p.species_id, p.name, p.level, p.points,
		        (SELECT MAX(created_at) FROM point_logs pl WHERE pl.pet_id = p.id),
		        p.currency, p.active_scene
		 FROM students s LEFT JOIN pets p ON p.student_id = s.id
		 WHERE s.class_id = ? AND s.deleted_at IS NULL
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
			petName       sql.NullString
			level, points sql.NullInt64
			lastPoints    sql.NullString
			currency      sql.NullInt64
			activeScene   sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.StudentNo, &e.Name, &petID, &speciesID, &petName, &level, &points, &lastPoints,
			&currency, &activeScene); err != nil {
			writeInternal(w, err, "scan roster")
			return
		}
		if petID.Valid {
			e.Adopted = true
			e.Level = int(level.Int64)
			e.Points = int(points.Int64)
			e.LastPointsAt = lastPoints.String
			e.Currency = int(currency.Int64)
			e.ActiveScene = activeScene.String
			e.PetName = petName.String
			if sp, ok := speciesByID[speciesID.String]; ok {
				e.SpeciesID = sp.ID
				e.SpeciesName = sp.Name
				// #32 W1：按当前等级下发阶段图与剪影直链。
				e.ImageURL = speciesImageURL(sp, e.Level)
				e.Silhouette = sp.Silhouette
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

// ---------- 教师改宠物名 ----------

type teacherRenameRequest struct {
	Name string `json:"name"`
}

// handleTeacherRenamePet 教师改宠物名：可随时多次改（M6 移除 M1 一次限制）。
func (s *srv) handleTeacherRenamePet(w http.ResponseWriter, r *http.Request) {
	classID := r.Context().Value(ctxKeyTeacher).(int64)
	studentID, ok := pathID(w, r, "studentID")
	if !ok {
		return
	}

	var req teacherRenameRequest
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
	if utf8.RuneCountInString(name) > 24 {
		writeJSON(w, http.StatusBadRequest, errJSON("name 不能超过 24 个字符"))
		return
	}

	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	var live int
	err := s.db.QueryRow(
		`SELECT 1 FROM students WHERE id = ? AND class_id = ? AND deleted_at IS NULL`,
		studentID, classID,
	).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("学生不存在"))
		return
	}
	if err != nil {
		writeInternal(w, err, "load student")
		return
	}

	var (
		petID, level, points int64
		speciesID            string
	)
	err = s.db.QueryRow(
		`SELECT id, species_id, level, points FROM pets WHERE student_id = ?`, studentID,
	).Scan(&petID, &speciesID, &level, &points)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errJSON("该学生还没有宠物"))
		return
	}
	if err != nil {
		writeInternal(w, err, "load pet")
		return
	}
	if _, err := s.db.Exec(
		`UPDATE pets SET name = ?, name_customized = 1 WHERE id = ?`, name, petID,
	); err != nil {
		writeInternal(w, err, "rename pet")
		return
	}
	sp, ok := speciesByID[speciesID]
	if !ok {
		writeInternal(w, errors.New("species not found: "+speciesID), "load pet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pet": petJSON(petID, name, int(level), int(points), sp, s.levels),
	})
}

// ---------- 共用小工具 ----------

// liveStudentID 解析教师班级内在册学生的 id；不存在（或已删）返回 0。
func liveStudentID(tx *sql.Tx, classID int64, studentNo string) (int64, error) {
	var id int64
	err := tx.QueryRow(
		`SELECT id FROM students WHERE class_id = ? AND student_no = ? AND deleted_at IS NULL`,
		classID, studentNo,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// pathID 解析路径中的正整数 id 参数（非法时已写好 400 响应）。
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	v := r.PathValue(name)
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errJSON("路径参数 %s 非法", name))
		return 0, false
	}
	return id, true
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// isUniqueConstraintErr 判断是否 SQLite 唯一约束冲突。
func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// randomDigits 生成 n 位随机数字串。
func randomDigits(n int) string {
	buf := make([]byte, n)
	for i := range buf {
		b, err := crand.Int(crand.Reader, big.NewInt(10))
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		buf[i] = byte('0' + b.Int64())
	}
	return string(buf)
}
