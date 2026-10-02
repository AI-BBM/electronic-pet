package server_test

// 统计与并发用例（T11–T13）。
// server.New 不暴露随机注入，T11 用 400 个不同学生各 adopt 一次做分布断言（宽容差防 flaky）。

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// T11 权重统计：400 个学生各 adopt 一次（eggId="random"），
// rarity 全部合法且分布落在宽容差区间，覆盖种类数 ≥10/12。
func TestAdoptRarityWeightsAndSpeciesCoverage(t *testing.T) {
	h := newHandler(t)

	const total = 400
	rarityCount := map[string]int{}
	speciesSeen := map[string]int{}

	for i := 0; i < total; i++ {
		studentNo := fmt.Sprintf("w%03d", i)
		res := joinStudent(h, "weight-class", "同学", studentNo)
		if res.Err != nil {
			t.Fatalf("学生 %s join 失败: %v", studentNo, res.Err)
		}
		if res.Status != http.StatusOK {
			t.Fatalf("学生 %s join 状态码 = %d, 期望 200, body=%v", studentNo, res.Status, res.Body)
		}
		status, raw := adoptEgg(h, res.Token, "random")
		if status != http.StatusOK {
			t.Fatalf("学生 %s adopt 状态码 = %d, 期望 200, body=%s", studentNo, status, raw)
		}
		p := petFromEnvelope(t, raw)
		if p.Species.ID == "" {
			t.Fatalf("学生 %s 的宠物 species.id 为空", studentNo)
		}
		if p.Species.ImageURL == "" {
			t.Fatalf("学生 %s 的宠物 species.imageUrl 为空", studentNo)
		}
		if !isValidRarity(p.Species.Rarity) {
			t.Fatalf("学生 %s 的宠物 rarity = %q, 应为 common/rare/epic 之一", studentNo, p.Species.Rarity)
		}
		rarityCount[p.Species.Rarity]++
		speciesSeen[p.Species.ID]++
	}

	assertRange := func(rarity string, low, high int) {
		got := rarityCount[rarity]
		if got < low || got > high {
			t.Errorf("%s 数量 = %d（%.1f%%）, 期望区间 [%d, %d]（共 %d 次）",
				rarity, got, float64(got)/float64(total)*100, low, high, total)
		}
	}
	assertRange("common", 240, 320) // 60%–80%（名义 70%）
	assertRange("rare", 60, 140)    // 15%–35%（名义 25%）
	assertRange("epic", 4, 40)      // 1%–10%（名义 5%）

	if len(speciesSeen) < 10 {
		t.Errorf("覆盖种类数 = %d, 期望 ≥10/12, 实际命中: %v", len(speciesSeen), speciesSeen)
	}
	if t.Failed() {
		t.Logf("实际分布: common=%d rare=%d epic=%d 种类数=%d",
			rarityCount["common"], rarityCount["rare"], rarityCount["epic"], len(speciesSeen))
	}
}

// T12 并发 50 个不同学生同时 join+adopt：全部 200，各自恰好一只宠物，无 5xx。
func TestConcurrentAdoptFiftyStudents(t *testing.T) {
	h := newHandler(t)

	const students = 50
	tokens := make([]string, students)
	problems := make([]string, students)

	var wg sync.WaitGroup
	for i := 0; i < students; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := joinStudent(h, "cc50", "同学", fmt.Sprintf("s%02d", i))
			if res.Err != nil {
				problems[i] = fmt.Sprintf("join: %v", res.Err)
				return
			}
			if res.Status != http.StatusOK {
				problems[i] = fmt.Sprintf("join status=%d body=%v", res.Status, res.Body)
				return
			}
			tokens[i] = res.Token
			status, raw := adoptEgg(h, res.Token, "random")
			if status != http.StatusOK {
				problems[i] = fmt.Sprintf("adopt status=%d body=%s", status, raw)
			}
		}(i)
	}
	wg.Wait()

	for i, p := range problems {
		if p != "" {
			t.Errorf("学生 %d: %s", i, p)
		}
	}
	for i, token := range tokens {
		if token == "" {
			t.Errorf("学生 %d 未取得 token", i)
		}
	}

	// 复核：每个学生的 /api/pet/me 都恰好返回一只宠物。
	for i, token := range tokens {
		if token == "" {
			continue
		}
		resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("学生 %d GET /api/pet/me 状态码 = %d, 期望 200, body=%v", i, resp.StatusCode, body)
			continue
		}
		if body["pet"] == nil {
			t.Errorf("学生 %d GET /api/pet/me 未返回宠物", i)
		}
	}
}

// T13 同一学生 20 并发 adopt：恰好 1 个 200、其余 19 个 409，禁止 5xx。
func TestConcurrentAdoptSameStudentSinglePet(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "cc1", "小明", "01")

	const attempts = 20
	statuses := make([]int, attempts)
	bodies := make([]string, attempts)

	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, raw := adoptEgg(h, token, "random")
			statuses[i] = status
			bodies[i] = string(raw)
		}(i)
	}
	wg.Wait()

	okCount, conflictCount := 0, 0
	for i, status := range statuses {
		switch status {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
			conflictCount++
		default:
			t.Errorf("第 %d 次 adopt 状态码 = %d（只允许 200/409，禁止 5xx）, body=%s", i, status, bodies[i])
		}
	}
	if okCount != 1 {
		t.Errorf("成功 adopt 次数 = %d, 期望恰好 1", okCount)
	}
	if conflictCount != attempts-1 {
		t.Errorf("409 次数 = %d, 期望 %d", conflictCount, attempts-1)
	}

	// 最终状态：该学生名下恰好有一只宠物。
	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pet/me 状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
	}
	if body["pet"] == nil {
		t.Errorf("GET /api/pet/me 未返回那一只宠物")
	}
}
