package server

import (
	"log"
	"time"
)

// trashCleanupInterval 是垃圾桶清理任务的执行间隔（每日）。
const trashCleanupInterval = 24 * time.Hour

// startTrashCleanupLoop 启动每日清理循环（随服务进程生命周期）。
func (s *srv) startTrashCleanupLoop() {
	go func() {
		ticker := time.NewTicker(trashCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.cleanupExpiredStudents(time.Now())
		}
	}()
}

// cleanupExpiredStudents 硬删除垃圾桶中超期（deleted_at 早于 now-3 个月）的学生
// 及其宠物、流水。删除顺序 point_logs → pets → students 满足外键；事务内执行。
// 启动时（New）与每日各执行一次；now 注入以便测试。
func (s *srv) cleanupExpiredStudents(now time.Time) {
	cutoff := now.AddDate(0, 0, -trashRetentionDays).Unix()

	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("[trash-cleanup] begin: %v", err)
		return
	}
	defer tx.Rollback()

	var expiredIDs []int64
	rows, err := tx.Query(
		`SELECT id FROM students WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cutoff,
	)
	if err != nil {
		log.Printf("[trash-cleanup] list expired: %v", err)
		return
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			log.Printf("[trash-cleanup] scan: %v", err)
			return
		}
		expiredIDs = append(expiredIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("[trash-cleanup] rows: %v", err)
		return
	}
	if len(expiredIDs) == 0 {
		return
	}

	for _, id := range expiredIDs {
		if _, err := tx.Exec(
			`DELETE FROM point_logs WHERE pet_id IN (SELECT id FROM pets WHERE student_id = ?)`, id,
		); err != nil {
			log.Printf("[trash-cleanup] delete logs (student %d): %v", id, err)
			return
		}
		if _, err := tx.Exec(`DELETE FROM pets WHERE student_id = ?`, id); err != nil {
			log.Printf("[trash-cleanup] delete pets (student %d): %v", id, err)
			return
		}
		if _, err := tx.Exec(`DELETE FROM students WHERE id = ?`, id); err != nil {
			log.Printf("[trash-cleanup] delete student %d: %v", id, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[trash-cleanup] commit: %v", err)
		return
	}
	log.Printf("[trash-cleanup] purged %d expired students", len(expiredIDs))
}
