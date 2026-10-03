package server

import (
	"net/http"
)

// dexEntry 是 /api/dex 中单一种类的响应形状。
// 未解锁条目的 stages 必须为显式 null（验收红线：不泄露彩色立绘 URL）。
type dexEntry struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Rarity     string   `json:"rarity"`
	Unlocked   bool     `json:"unlocked"`
	Owners     int      `json:"owners"`
	Stages     []string `json:"stages"`
	Silhouette string   `json:"silhouette"`
}

// handleDex 图鉴：按 token 学生所在班级计算各物种解锁状态与拥有数。
func (s *srv) handleDex(w http.ResponseWriter, r *http.Request) {
	studentID := r.Context().Value(ctxKeyStudent).(int64)

	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	var classID int64
	if err := s.db.QueryRow(`SELECT class_id FROM students WHERE id = ?`, studentID).Scan(&classID); err != nil {
		writeInternal(w, err, "load student class")
		return
	}
	rows, err := s.db.Query(
		`SELECT species_id, COUNT(*) FROM pets p
		 JOIN students st ON p.student_id = st.id
		 WHERE st.class_id = ?
		 GROUP BY species_id`, classID)
	if err != nil {
		writeInternal(w, err, "count species owners")
		return
	}
	defer rows.Close()
	ownersBySpecies := make(map[string]int, len(speciesList))
	for rows.Next() {
		var speciesID string
		var n int
		if err := rows.Scan(&speciesID, &n); err != nil {
			writeInternal(w, err, "scan species owners")
			return
		}
		ownersBySpecies[speciesID] = n
	}
	if err := rows.Err(); err != nil {
		writeInternal(w, err, "count species owners")
		return
	}

	entries := make([]dexEntry, 0, len(speciesList))
	for _, sp := range speciesList {
		owners := ownersBySpecies[sp.ID]
		entry := dexEntry{
			ID:         sp.ID,
			Name:       sp.Name,
			Rarity:     sp.Rarity,
			Unlocked:   owners > 0,
			Owners:     owners,
			Silhouette: sp.Silhouette,
		}
		if entry.Unlocked {
			entry.Stages = sp.Stages
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"species": entries})
}
