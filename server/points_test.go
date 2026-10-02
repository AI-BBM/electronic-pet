package server_test

// M2 加分 API 黑盒用例（T2/T3/T4/T5/T7/T10/T11）。
// 契约来源：docs/product/features/m2-points-levelup.md + 测试先行任务书。

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/AI-BBM/electronic-pet/server"
)

// T2 PRD 验收：19 分 + 1 分触发 Lv2，档案与流水立即一致。
// 命令: go test ./server/ -run TestAddPoints_CrossesLv2 -v
func TestAddPoints_CrossesLv2(t *testing.T) {
	h := newHandler(t)
	token, adopted := m2JoinedToken(t, h)

	m2SeedPoints(t, h, token, 19)

	res := m2AddPoints(h, token, m2PointsRequest{Reason: "作业优秀", Value: 1, RequestID: "t2-final"})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /api/points 状态码 = %d, 期望 200; body=%v", res.Status, res.Body)
	}
	if !res.Added {
		t.Error("added = false, 期望 true")
	}
	if !res.LevelUp {
		t.Error("levelUp = false, 期望 true（19+1 跨过 Lv2 阈值 20）")
	}
	if res.Level != 2 || res.Pet.Level != 2 {
		t.Errorf("level = %d / pet.level = %d, 期望均为 2", res.Level, res.Pet.Level)
	}
	if res.Pet.Points != 20 {
		t.Errorf("pet.points = %d, 期望 20", res.Pet.Points)
	}
	if res.Pet.NextLevelPoints == nil || *res.Pet.NextLevelPoints != 60 {
		t.Errorf("pet.nextLevelPoints = %v, 期望指向 60", res.Pet.NextLevelPoints)
	}
	checkPetShape(t, res.Pet)
	if adopted.Name == "" || res.Pet.Name == "" {
		t.Error("pet.name 为空")
	}

	// 档案（GET /api/pet/me）与加分响应立即一致
	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pet/me 状态码 = %d, 期望 200; body=%v", resp.StatusCode, body)
	}
	if me := petFromAny(t, body["pet"]); me.Points != 20 || me.Level != 2 {
		t.Errorf("档案 = (level %d, points %d), 期望 (2, 20)", me.Level, me.Points)
	}

	// 流水立即可读且内容正确（首条为最后一次加分）
	status, list, raw := m2GetLog(h, token, "?page=1")
	if status != http.StatusOK {
		t.Fatalf("GET log 状态码 = %d, 期望 200; body=%s", status, raw)
	}
	if list.Total != 20 || len(list.Items) != 20 {
		t.Fatalf("流水 total=%d items=%d, 期望 20/20", list.Total, len(list.Items))
	}
	first := list.Items[0]
	if first.Value != 1 || first.Reason != "作业优秀" {
		t.Errorf("流水首条 = %+v, 期望 value=1 reason=作业优秀", first)
	}
	if first.CreatedAt == "" {
		t.Error("流水 createdAt 为空, 期望非空")
	}
}

// T3 满级路径：59+1 触发 Lv3；满级后继续积分但不再升级，nextLevelPoints 为 null。
// 命令: go test ./server/ -run TestAddPoints_CrossesLv3_ThenStaysMax -v
func TestAddPoints_CrossesLv3_ThenStaysMax(t *testing.T) {
	h := newHandler(t)
	token, _ := m2JoinedToken(t, h)

	// 精确累计到 59：5×10 + 9×1
	for i := 0; i < 5; i++ {
		if res := m2AddPoints(h, token, m2PointsRequest{Reason: "作业优秀", Value: 10, RequestID: fmt.Sprintf("t3-a%d", i)}); res.Status != http.StatusOK || !res.Added {
			t.Fatalf("seed 10 分第 %d 次失败: %v", i, res.Body)
		}
	}
	m2SeedPoints(t, h, token, 9)

	res := m2AddPoints(h, token, m2PointsRequest{Reason: "进步之星", Value: 1, RequestID: "t3-final"})
	if res.Status != http.StatusOK {
		t.Fatalf("第一次加分状态码 = %d, 期望 200; body=%v", res.Status, res.Body)
	}
	if !res.LevelUp || res.Level != 3 || res.Pet.Points != 60 {
		t.Errorf("59+1 后 = (levelUp=%v level=%d points=%d), 期望 (true, 3, 60)",
			res.LevelUp, res.Level, res.Pet.Points)
	}
	if res.Pet.NextLevelPoints != nil {
		t.Errorf("满级后 nextLevelPoints = %v, 期望 nil", *res.Pet.NextLevelPoints)
	}

	// 满级后继续加分：积分继续累计，但 levelUp=false、等级保持 3
	res = m2AddPoints(h, token, m2PointsRequest{Reason: "劳动卫生", Value: 2, RequestID: "t3-max"})
	if res.Status != http.StatusOK {
		t.Fatalf("满级后加分状态码 = %d, 期望 200; body=%v", res.Status, res.Body)
	}
	if !res.Added {
		t.Error("满级后 added = false, 期望 true（满级继续积分）")
	}
	if res.LevelUp {
		t.Error("满级后 levelUp = true, 期望 false")
	}
	if res.Level != 3 || res.Pet.Level != 3 {
		t.Errorf("满级后 level = %d / pet.level = %d, 期望均为 3", res.Level, res.Pet.Level)
	}
	if res.Pet.Points != 62 {
		t.Errorf("满级后 points = %d, 期望 62（继续累计）", res.Pet.Points)
	}
	if res.Pet.NextLevelPoints != nil {
		t.Errorf("满级后 nextLevelPoints = %v, 期望 nil", *res.Pet.NextLevelPoints)
	}

	// 流水 16 条（seed 14 次请求 = 5×10+9×1，加两次正式加分）
	if _, list, _ := m2GetLog(h, token, "?page=1"); list.Total != 16 {
		t.Errorf("流水条数 = %d, 期望 16", list.Total)
	}
}

// T4 幂等：同 pet 同 requestId 重复提交不重复计分，重放返回当前状态。
// 命令: go test ./server/ -run TestAddPoints_IdempotentByRequestID -v
func TestAddPoints_IdempotentByRequestID(t *testing.T) {
	h := newHandler(t)
	token, _ := m2JoinedToken(t, h)

	first := m2PointsRequest{Reason: "作业优秀", Value: 5, RequestID: "dup-1"}
	res := m2AddPoints(h, token, first)
	if res.Status != http.StatusOK || !res.Added || res.Pet.Points != 5 {
		t.Fatalf("首次提交 = (status=%d added=%v points=%d), 期望 (200 true 5)", res.Status, res.Added, res.Pet.Points)
	}

	// 完全相同的重复提交 → 200 + added=false，积分不变
	res = m2AddPoints(h, token, first)
	if res.Status != http.StatusOK {
		t.Fatalf("重复提交状态码 = %d, 期望 200; body=%v", res.Status, res.Body)
	}
	if res.Added || res.LevelUp {
		t.Errorf("重复提交 = (added=%v levelUp=%v), 期望 (false false)", res.Added, res.LevelUp)
	}
	if res.Pet.Points != 5 || res.Level != 1 {
		t.Errorf("重复提交后 (points=%d level=%d), 期望 (5, 1)", res.Pet.Points, res.Level)
	}

	// 同 requestId 第三次提交（即便参数不同）同样不重复计分
	res = m2AddPoints(h, token, m2PointsRequest{Reason: "课堂表现", Value: 3, RequestID: "dup-1"})
	if res.Added || res.Pet.Points != 5 {
		t.Errorf("第三次重放 = (added=%v points=%d), 期望 (false 5)", res.Added, res.Pet.Points)
	}

	// 流水不因重放而增长
	if status, list, raw := m2GetLog(h, token, ""); status != http.StatusOK || list.Total != 1 {
		t.Fatalf("流水 total = %d (status %d), 期望 1; body=%s", list.Total, status, raw)
	}

	// 不同 requestId 的新提交正常计分
	res = m2AddPoints(h, token, m2PointsRequest{Reason: "进步之星", Value: 1, RequestID: "dup-2"})
	if !res.Added || res.Pet.Points != 6 {
		t.Errorf("新 requestId = (added=%v points=%d), 期望 (true 6)", res.Added, res.Pet.Points)
	}
	if _, list, _ := m2GetLog(h, token, ""); list.Total != 2 {
		t.Errorf("新 requestId 后流水条数 = %d, 期望 2", list.Total)
	}
}

// T5 参数校验：value 越界（0/11/负数）与 reason 非法（空/纯空白/超 100 字符）一律 400；边界 1 与 10 合法。
// 命令: go test ./server/ -run TestAddPoints_Validation -v
func TestAddPoints_Validation(t *testing.T) {
	h := newHandler(t)
	token, _ := m2JoinedToken(t, h)

	longReason := strings.Repeat("优", 101)
	cases := []struct {
		name string
		req  m2PointsRequest
		want int
	}{
		{"value=0 越界", m2PointsRequest{Reason: "作业优秀", Value: 0}, http.StatusBadRequest},
		{"value=11 越界", m2PointsRequest{Reason: "作业优秀", Value: 11}, http.StatusBadRequest},
		{"value=-3 越界", m2PointsRequest{Reason: "作业优秀", Value: -3}, http.StatusBadRequest},
		{"reason 为空", m2PointsRequest{Reason: "", Value: 1}, http.StatusBadRequest},
		{"reason 纯空白", m2PointsRequest{Reason: "   \t", Value: 1}, http.StatusBadRequest},
		{"reason 超 100 字符", m2PointsRequest{Reason: longReason, Value: 1}, http.StatusBadRequest},
		{"合法边界 value=1", m2PointsRequest{Reason: "作业优秀", Value: 1}, http.StatusOK},
		{"合法边界 value=10", m2PointsRequest{Reason: "作业优秀", Value: 10}, http.StatusOK},
	}
	for i, tc := range cases {
		tc.req.RequestID = fmt.Sprintf("t5-%d", i)
		res := m2AddPoints(h, token, tc.req)
		if res.Status != tc.want {
			t.Errorf("%s: 状态码 = %d, 期望 %d; body=%v", tc.name, res.Status, tc.want, res.Body)
		}
	}
}

// T7 学生无宠物时加分 → 404，且响应包含 error 字段。
// 命令: go test ./server/ -run TestAddPoints_StudentWithoutPet_NotFound -v
func TestAddPoints_StudentWithoutPet_NotFound(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c404", "李无宠", "S404")

	res := m2AddPoints(h, token, m2PointsRequest{Reason: "作业优秀", Value: 1, RequestID: "t7-1"})
	if res.Status != http.StatusNotFound {
		t.Fatalf("无宠物学生加分状态码 = %d, 期望 404; body=%v", res.Status, res.Body)
	}
	msg, ok := res.Body["error"].(string)
	if !ok || msg == "" {
		t.Errorf("404 响应应包含 error 字段; body=%v", res.Body)
	}
}

// T10 并发加分：20 个 goroutine 各加 1 分，最终恰好 20 分、20 条流水、无失败请求（go test -race）。
// 命令: go test -race ./server/ -run TestAddPoints_ConcurrentNoLostUpdate -v
func TestAddPoints_ConcurrentNoLostUpdate(t *testing.T) {
	h := newHandler(t)
	token, _ := m2JoinedToken(t, h)

	const n = 20
	var wg sync.WaitGroup
	errCh := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := m2AddPoints(h, token, m2PointsRequest{
				Reason: "课堂表现", Value: 1, RequestID: fmt.Sprintf("t10-%d", i),
			})
			if res.Status != http.StatusOK {
				errCh <- fmt.Sprintf("goroutine %d: 状态码 = %d, 期望 200", i, res.Status)
				return
			}
			if !res.Added {
				errCh <- fmt.Sprintf("goroutine %d: added = false, 期望 true", i)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}

	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pet/me 状态码 = %d; body=%v", resp.StatusCode, body)
	}
	me := petFromAny(t, body["pet"])
	if me.Points != n {
		t.Errorf("并发后最终积分 = %d, 期望 %d（存在丢更新）", me.Points, n)
	}
	if me.Level != 2 {
		t.Errorf("并发后最终等级 = %d, 期望 2（20 分恰好达 Lv2）", me.Level)
	}
	if _, list, _ := m2GetLog(h, token, "?page=1"); list.Total != n {
		t.Errorf("并发后流水条数 = %d, 期望 %d", list.Total, n)
	}
}

// T11 自定义阈值端到端：PET_LEVELS_FILE={"lv2":5,"lv3":10} 时 4+1 触发 Lv2、再 +5 触发 Lv3 满级。
// 命令: go test ./server/ -run TestAddPoints_CustomLevels_EndToEnd -v
func TestAddPoints_CustomLevels_EndToEnd(t *testing.T) {
	h := m2NewHandlerWithLevels(t, 5, 10)
	token, _ := m2JoinedToken(t, h)

	m2SeedPoints(t, h, token, 4)

	res := m2AddPoints(h, token, m2PointsRequest{Reason: "课堂表现", Value: 1, RequestID: "t11-a"})
	if !res.LevelUp || res.Level != 2 || res.Pet.Points != 5 {
		t.Fatalf("自定义阈值 4+1 = (levelUp=%v level=%d points=%d), 期望 (true 2 5)", res.LevelUp, res.Level, res.Pet.Points)
	}
	if res.Pet.NextLevelPoints == nil || *res.Pet.NextLevelPoints != 10 {
		t.Errorf("nextLevelPoints = %v, 期望指向 10", res.Pet.NextLevelPoints)
	}

	res = m2AddPoints(h, token, m2PointsRequest{Reason: "作业优秀", Value: 5, RequestID: "t11-b"})
	if !res.LevelUp || res.Level != 3 || res.Pet.Points != 10 {
		t.Fatalf("再 +5 = (levelUp=%v level=%d points=%d), 期望 (true 3 10)", res.LevelUp, res.Level, res.Pet.Points)
	}
	if res.Pet.NextLevelPoints != nil {
		t.Errorf("满级 nextLevelPoints = %v, 期望 nil", *res.Pet.NextLevelPoints)
	}
}

// 确认 server 包导出阈值配置入口（编译期锚点，防误删导出符号）。
var _ = server.DefaultLevels
var _ = server.LevelsFileEnv
