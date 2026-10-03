package server

import (
	"database/sql"
	"errors"
	"net/http"
)

// wallEntry 是 /api/class/wall 中单只宠物的公开档案形状。
// 不含 studentNo（学号是身份键，不上墙）。
type wallEntry struct {
	PetID          int64  `json:"petId"`
	PetName        string `json:"petName"`
	StudentName    string `json:"studentName"`
	SpeciesID      string `json:"speciesId"`
	SpeciesName    string `json:"speciesName"`
	Rarity         string `json:"rarity"`
	Level          int    `json:"level"`
	Points         int    `json:"points"`
	ImageURL       string `json:"imageUrl"`
	LatestReason   string `json:"latestReason"`
	LatestActivity any    `json:"latestActivity"`
}

// handleWall 班级墙：本班已孵出宠物的排行，sort=points（默认，积分降序）
// 或 recent（最近升级动态：最新流水自增 id 降序，规避同秒时钟粒度，无流水排最后）。
func (s *srv) handleWall(w http.ResponseWriter, r *http.Request) {
	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "points"
	}
	if sort != "points" && sort != "recent" {
		writeJSON(w, http.StatusBadRequest, errJSON("sort 仅支持 points 或 recent"))
		return
	}
	studentID := r.Context().Value(ctxKeyStudent).(int64)

	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	var classID int64
	if err := s.db.QueryRow(`SELECT class_id FROM students WHERE id = ?`, studentID).Scan(&classID); err != nil {
		writeInternal(w, err, "load student class")
		return
	}

	// 最新流水用相关子查询按 pet 取（idx_point_logs_pet 支撑，天然限定本班宠物行），
	// id 单调递增，规避 created_at 同秒并列。
	const query = `
SELECT p.id, p.name, p.level, p.points, p.species_id, st.name,
       (SELECT reason FROM point_logs WHERE pet_id = p.id ORDER BY id DESC LIMIT 1),
       (SELECT created_at FROM point_logs WHERE pet_id = p.id ORDER BY id DESC LIMIT 1),
       (SELECT MAX(id) FROM point_logs WHERE pet_id = p.id)
FROM pets p
JOIN students st ON p.student_id = st.id
WHERE st.class_id = ?
ORDER BY `

	var orderBy string
	if sort == "points" {
		orderBy = "p.points DESC, p.id ASC"
	} else {
		// SQLite 中 NULL 最小：(last_id IS NULL) 升序把无流水者排到最后
		orderBy = "(SELECT MAX(id) FROM point_logs WHERE pet_id = p.id) IS NULL ASC," +
			" (SELECT MAX(id) FROM point_logs WHERE pet_id = p.id) DESC, p.id ASC"
	}

	rows, err := s.db.Query(query+orderBy, classID)
	if err != nil {
		writeInternal(w, err, "list wall")
		return
	}
	defer rows.Close()

	wall := []wallEntry{}
	for rows.Next() {
		var (
			e         wallEntry
			speciesID string
			reason    sql.NullString
			activity  sql.NullString
			lastID    sql.NullInt64
		)
		if err := rows.Scan(&e.PetID, &e.PetName, &e.Level, &e.Points, &speciesID, &e.StudentName, &reason, &activity, &lastID); err != nil {
			writeInternal(w, err, "scan wall")
			return
		}
		sp, ok := speciesByID[speciesID]
		if !ok {
			writeInternal(w, errors.New("species not found: "+speciesID), "scan wall")
			return
		}
		e.SpeciesID = sp.ID
		e.SpeciesName = sp.Name
		e.Rarity = sp.Rarity
		e.ImageURL = speciesImageURL(sp, e.Level)
		e.LatestReason = reason.String
		if activity.Valid {
			e.LatestActivity = activity.String
		}
		wall = append(wall, e)
	}
	if err := rows.Err(); err != nil {
		writeInternal(w, err, "list wall")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sort": sort, "wall": wall})
}
