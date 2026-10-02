package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// pointsPostReq 是 POST /api/points 的请求体形状。
type pointsPostReq struct {
	Reason    string `json:"reason"`
	Value     int    `json:"value"`
	RequestID string `json:"requestId,omitempty"`
}

// petJSONView 是响应中 pet 字段的形状；NextLevelPoints 满级时为 JSON null。
type petJSONView struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Species         string `json:"species"`
	Rarity          string `json:"rarity"`
	Level           int    `json:"level"`
	Points          int    `json:"points"`
	NextLevelPoints *int   `json:"nextLevelPoints"`
}

// pointsPostResp 是 POST /api/points 的响应体形状。
type pointsPostResp struct {
	Pet     petJSONView `json:"pet"`
	LevelUp bool        `json:"levelUp"`
	Level   int         `json:"level"`
	Added   bool        `json:"added"`
}

// T2 PRD 验收：19 分 + 1 分触发 Lv2，档案与流水立即一致。
// 命令: go test ./server/ -run TestAddPoints_CrossesLv2 -v
func TestAddPoints_CrossesLv2(t *testing.T) {
	env := newTestEnv(t)
	db := env.store.DB()
	studentID, petID := seedStudentWithPet(t, db, 1, 19)

	status, body := env.do(t, http.MethodPost, "/api/points", env.token(studentID),
		pointsPostReq{Reason: "作业优秀", Value: 1, RequestID: "t2-1"})
	if status != http.StatusOK {
		t.Fatalf("POST /api/points 状态码 = %d, 期望 200; body=%s", status, body)
	}
	resp := decodePointsResp(t, body)
	if !resp.Added {
		t.Error("added = false, 期望 true")
	}
	if !resp.LevelUp {
		t.Error("levelUp = false, 期望 true（19+1 跨过 Lv2 阈值 20）")
	}
	if resp.Level != 2 {
		t.Errorf("level = %d, 期望 2", resp.Level)
	}
	if resp.Pet.ID != petID {
		t.Errorf("pet.id = %d, 期望 %d", resp.Pet.ID, petID)
	}
	if resp.Pet.Species != "robotcat" || resp.Pet.Rarity != "N" || resp.Pet.Name != "测试宠物" {
		t.Errorf("pet 档案字段回显错误: %+v", resp.Pet)
	}
	if resp.Pet.Level != 2 {
		t.Errorf("pet.level = %d, 期望 2", resp.Pet.Level)
	}
	if resp.Pet.Points != 20 {
		t.Errorf("pet.points = %d, 期望 20", resp.Pet.Points)
	}
	if resp.Pet.NextLevelPoints == nil || *resp.Pet.NextLevelPoints != 60 {
		t.Errorf("pet.nextLevelPoints = %v, 期望指向 60", resp.Pet.NextLevelPoints)
	}
	if raw, ok := rawPetField(t, body, "nextLevelPoints"); !ok || raw != "60" {
		t.Errorf("pet.nextLevelPoints 原始 JSON = %q(ok=%v), 期望 \"60\"", raw, ok)
	}

	// 档案（DB）与响应立即一致
	level, points := petState(t, db, petID)
	if level != 2 || points != 20 {
		t.Errorf("DB 中宠物 = (level %d, points %d), 期望 (2, 20)", level, points)
	}
	if n := logCount(t, db, petID); n != 1 {
		t.Errorf("流水条数 = %d, 期望 1", n)
	}

	// 流水立即可读且内容正确
	status, body = env.do(t, http.MethodGet, "/api/pet/me/log?page=1", env.token(studentID), nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/pet/me/log 状态码 = %d, 期望 200; body=%s", status, body)
	}
	logResp := decodeLogList(t, body)
	if logResp.Total != 1 || len(logResp.Items) != 1 {
		t.Fatalf("流水 total=%d items=%d, 期望 1/1", logResp.Total, len(logResp.Items))
	}
	if logResp.Items[0].Value != 1 || logResp.Items[0].Reason != "作业优秀" {
		t.Errorf("流水首条 = %+v, 期望 value=1 reason=作业优秀", logResp.Items[0])
	}
	if logResp.Items[0].CreatedAt == "" {
		t.Error("流水 createdAt 为空字符串, 期望非空")
	}
}

// T3 满级路径：59+1 触发 Lv3；满级后继续积分但不再升级，nextLevelPoints 为 null。
// 命令: go test ./server/ -run TestAddPoints_CrossesLv3_ThenStaysMax -v
func TestAddPoints_CrossesLv3_ThenStaysMax(t *testing.T) {
	env := newTestEnv(t)
	db := env.store.DB()
	studentID, petID := seedStudentWithPet(t, db, 2, 59)

	status, body := env.do(t, http.MethodPost, "/api/points", env.token(studentID),
		pointsPostReq{Reason: "进步之星", Value: 1, RequestID: "t3-1"})
	if status != http.StatusOK {
		t.Fatalf("第一次加分状态码 = %d, 期望 200; body=%s", status, body)
	}
	resp := decodePointsResp(t, body)
	if !resp.LevelUp || resp.Level != 3 || resp.Pet.Points != 60 {
		t.Errorf("59+1 后 = (levelUp=%v level=%d points=%d), 期望 (true, 3, 60)",
			resp.LevelUp, resp.Level, resp.Pet.Points)
	}
	if raw, ok := rawPetField(t, body, "nextLevelPoints"); !ok || raw != "null" {
		t.Errorf("满级后 nextLevelPoints 原始 JSON = %q(ok=%v), 期望 \"null\"", raw, ok)
	}

	// 满级后继续加分：积分继续累计，但 levelUp=false、等级保持 3、nextLevelPoints 仍为 null
	status, body = env.do(t, http.MethodPost, "/api/points", env.token(studentID),
		pointsPostReq{Reason: "劳动卫生", Value: 2, RequestID: "t3-2"})
	if status != http.StatusOK {
		t.Fatalf("满级后加分状态码 = %d, 期望 200; body=%s", status, body)
	}
	resp = decodePointsResp(t, body)
	if !resp.Added {
		t.Error("满级后 added = false, 期望 true（满级继续积分）")
	}
	if resp.LevelUp {
		t.Error("满级后 levelUp = true, 期望 false")
	}
	if resp.Level != 3 || resp.Pet.Level != 3 {
		t.Errorf("满级后 level = %d / pet.level = %d, 期望均为 3", resp.Level, resp.Pet.Level)
	}
	if resp.Pet.Points != 62 {
		t.Errorf("满级后 points = %d, 期望 62（继续累计）", resp.Pet.Points)
	}
	if resp.Pet.NextLevelPoints != nil {
		t.Errorf("满级后 nextLevelPoints = %d, 期望 nil", *resp.Pet.NextLevelPoints)
	}
	if raw, ok := rawPetField(t, body, "nextLevelPoints"); !ok || raw != "null" {
		t.Errorf("满级后 nextLevelPoints 原始 JSON = %q(ok=%v), 期望 \"null\"", raw, ok)
	}

	level, points := petState(t, db, petID)
	if level != 3 || points != 62 {
		t.Errorf("DB 中宠物 = (level %d, points %d), 期望 (3, 62)", level, points)
	}
	if n := logCount(t, db, petID); n != 2 {
		t.Errorf("流水条数 = %d, 期望 2", n)
	}
}

// T4 幂等：同 pet 同 requestId 重复提交不重复计分，重放返回当前状态。
// 命令: go test ./server/ -run TestAddPoints_IdempotentByRequestID -v
func TestAddPoints_IdempotentByRequestID(t *testing.T) {
	env := newTestEnv(t)
	db := env.store.DB()
	studentID, petID := seedStudentWithPet(t, db, 1, 0)
	token := env.token(studentID)

	first := pointsPostReq{Reason: "作业优秀", Value: 5, RequestID: "dup-1"}
	status, body := env.do(t, http.MethodPost, "/api/points", token, first)
	if status != http.StatusOK {
		t.Fatalf("首次提交状态码 = %d, 期望 200; body=%s", status, body)
	}
	if resp := decodePointsResp(t, body); !resp.Added || resp.Pet.Points != 5 {
		t.Fatalf("首次提交 = (added=%v points=%d), 期望 (true, 5)", resp.Added, resp.Pet.Points)
	}

	// 完全相同的重复提交 → 200 + added=false，积分不变
	status, body = env.do(t, http.MethodPost, "/api/points", token, first)
	if status != http.StatusOK {
		t.Fatalf("重复提交状态码 = %d, 期望 200; body=%s", status, body)
	}
	resp := decodePointsResp(t, body)
	if resp.Added {
		t.Error("重复提交 added = true, 期望 false")
	}
	if resp.LevelUp {
		t.Error("重复提交 levelUp = true, 期望 false")
	}
	if resp.Pet.Points != 5 || resp.Level != 1 {
		t.Errorf("重复提交后 (points=%d level=%d), 期望 (5, 1)", resp.Pet.Points, resp.Level)
	}

	// 同 requestId 第三次提交（即便参数不同）同样不重复计分
	status, body = env.do(t, http.MethodPost, "/api/points", token,
		pointsPostReq{Reason: "课堂表现", Value: 3, RequestID: "dup-1"})
	if status != http.StatusOK {
		t.Fatalf("第三次重放状态码 = %d, 期望 200; body=%s", status, body)
	}
	if resp := decodePointsResp(t, body); resp.Added || resp.Pet.Points != 5 {
		t.Errorf("第三次重放 = (added=%v points=%d), 期望 (false, 5)", resp.Added, resp.Pet.Points)
	}

	if level, points := petState(t, db, petID); level != 1 || points != 5 {
		t.Errorf("DB 中宠物 = (level %d, points %d), 期望 (1, 5)", level, points)
	}
	if n := logCount(t, db, petID); n != 1 {
		t.Errorf("流水条数 = %d, 期望 1（重放不产生新流水）", n)
	}

	// 流水 total 不因重放而增长
	status, body = env.do(t, http.MethodGet, "/api/pet/me/log", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET log 状态码 = %d, 期望 200; body=%s", status, body)
	}
	if lr := decodeLogList(t, body); lr.Total != 1 {
		t.Errorf("流水 total = %d, 期望 1", lr.Total)
	}

	// 不同 requestId 的新提交正常计分
	status, body = env.do(t, http.MethodPost, "/api/points", token,
		pointsPostReq{Reason: "进步之星", Value: 1, RequestID: "dup-2"})
	if status != http.StatusOK {
		t.Fatalf("新 requestId 提交状态码 = %d, 期望 200; body=%s", status, body)
	}
	if resp := decodePointsResp(t, body); !resp.Added || resp.Pet.Points != 6 {
		t.Errorf("新 requestId = (added=%v points=%d), 期望 (true, 6)", resp.Added, resp.Pet.Points)
	}
	if n := logCount(t, db, petID); n != 2 {
		t.Errorf("新 requestId 后流水条数 = %d, 期望 2", n)
	}
}

// T5 参数校验：value 越界（0/11/负数）与 reason 非法（空/纯空白/超 100 字符）一律 400；边界 1 与 10 合法。
// 命令: go test ./server/ -run TestAddPoints_Validation -v
func TestAddPoints_Validation(t *testing.T) {
	env := newTestEnv(t)
	studentID, _ := seedStudentWithPet(t, env.store.DB(), 1, 0)
	token := env.token(studentID)

	longReason := strings.Repeat("优", 101)
	cases := []struct {
		name string
		req  pointsPostReq
		want int
	}{
		{"value=0 越界", pointsPostReq{Reason: "作业优秀", Value: 0}, http.StatusBadRequest},
		{"value=11 越界", pointsPostReq{Reason: "作业优秀", Value: 11}, http.StatusBadRequest},
		{"value=-3 越界", pointsPostReq{Reason: "作业优秀", Value: -3}, http.StatusBadRequest},
		{"reason 为空", pointsPostReq{Reason: "", Value: 1}, http.StatusBadRequest},
		{"reason 纯空白", pointsPostReq{Reason: "   \t", Value: 1}, http.StatusBadRequest},
		{"reason 超 100 字符", pointsPostReq{Reason: longReason, Value: 1}, http.StatusBadRequest},
		{"合法边界 value=1", pointsPostReq{Reason: "作业优秀", Value: 1}, http.StatusOK},
		{"合法边界 value=10", pointsPostReq{Reason: "作业优秀", Value: 10}, http.StatusOK},
	}
	for i, tc := range cases {
		req := tc.req
		req.RequestID = fmt.Sprintf("t5-%d", i)
		status, body := env.do(t, http.MethodPost, "/api/points", token, req)
		if status != tc.want {
			t.Errorf("%s: 状态码 = %d, 期望 %d; body=%s", tc.name, status, tc.want, body)
		}
	}
}

// T7 学生无宠物时加分 → 404，且响应包含 error 字段。
// 命令: go test ./server/ -run TestAddPoints_StudentWithoutPet_NotFound -v
func TestAddPoints_StudentWithoutPet_NotFound(t *testing.T) {
	env := newTestEnv(t)
	db := env.store.DB()
	classID := seedClass(t, db, "C404")
	studentID := seedStudent(t, db, classID, "李无宠", "S404")

	status, body := env.do(t, http.MethodPost, "/api/points", env.token(studentID),
		pointsPostReq{Reason: "作业优秀", Value: 1, RequestID: "t7-1"})
	if status != http.StatusNotFound {
		t.Fatalf("无宠物学生加分状态码 = %d, 期望 404; body=%s", status, body)
	}
	if !hasErrorField(t, body) {
		t.Errorf("404 响应应包含 error 字段; body=%s", body)
	}
}

// T10 并发加分：20 个 goroutine 各加 1 分，最终恰好 20 分、20 条流水、无失败请求（go test -race）。
// 命令: go test -race ./server/ -run TestAddPoints_ConcurrentNoLostUpdate -v
func TestAddPoints_ConcurrentNoLostUpdate(t *testing.T) {
	env := newTestEnv(t)
	db := env.store.DB()
	studentID, petID := seedStudentWithPet(t, db, 1, 0)
	token := env.token(studentID)

	const n = 20
	var wg sync.WaitGroup
	errCh := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := pointsPostReq{Reason: "课堂表现", Value: 1, RequestID: fmt.Sprintf("t10-%d", i)}
			status, body := env.do(t, http.MethodPost, "/api/points", token, req)
			if status != http.StatusOK {
				errCh <- fmt.Sprintf("goroutine %d: 状态码 = %d, 期望 200; body=%s", i, status, body)
				return
			}
			if resp := decodePointsResp(t, body); !resp.Added {
				errCh <- fmt.Sprintf("goroutine %d: added = false, 期望 true", i)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}

	level, points := petState(t, db, petID)
	if points != n {
		t.Errorf("并发后最终积分 = %d, 期望 %d（存在丢更新）", points, n)
	}
	if level != 2 {
		t.Errorf("并发后最终等级 = %d, 期望 2（20 分恰好达 Lv2）", level)
	}
	if c := logCount(t, db, petID); c != n {
		t.Errorf("并发后流水条数 = %d, 期望 %d", c, n)
	}
}
