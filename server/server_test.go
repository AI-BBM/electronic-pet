package server_test

// M1「领蛋与孵化」核心 API 黑盒用例（T1–T10）。
// 契约来源：PRD + 测试先行任务书。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// T1 join 成功：同一 classCode 首次进入自动建班建学生，返回 token，首次进入 pet 为 null。
func TestJoinCreatesClassAndStudent(t *testing.T) {
	h := newHandler(t)

	res := joinStudent(h, "c1", "小明", "01")
	if res.Err != nil {
		t.Fatalf("join 请求失败: %v", res.Err)
	}
	if res.Status != http.StatusOK {
		t.Fatalf("join 状态码 = %d, 期望 200, body=%v", res.Status, res.Body)
	}
	if res.Token == "" {
		t.Errorf("join 返回的 token 为空")
	}
	petRaw, present := res.Body["pet"]
	if !present {
		t.Fatalf("join 响应缺少 pet 字段: %v", res.Body)
	}
	if petRaw != nil {
		t.Errorf("首次 join 的 pet 应为 null, 实际: %v", petRaw)
	}
}

// T2 join 参数校验：班级码/姓名/学号任一缺失或全空白 → 400 + {"error":"..."}。
func TestJoinRejectsMissingOrBlankFields(t *testing.T) {
	h := newHandler(t)

	cases := []struct {
		name        string
		classCode   string
		studentName string
		studentNo   string
	}{
		{"缺班级码", "", "小明", "01"},
		{"缺姓名", "c1", "", "01"},
		{"缺学号", "c1", "小明", ""},
		{"班级码全空白", "   ", "小明", "01"},
		{"姓名全空白", "c1", " \t ", "01"},
		{"学号全空白", "c1", "小明", "  "},
		{"全部为空", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, h, http.MethodPost, "/api/join", "", map[string]string{
				"classCode": tc.classCode,
				"name":      tc.studentName,
				"studentNo": tc.studentNo,
			})
			expectError(t, resp.StatusCode, body, http.StatusBadRequest, tc.name)
		})
	}
}

// T3 同班级同学号幂等：不产生第二只宠物（adopt 前后各验证一次），新 token 身份同一。
func TestJoinIdempotentForSameStudent(t *testing.T) {
	h := newHandler(t)

	first := joinStudent(h, "c1", "小明", "01")
	if first.Err != nil || first.Status != http.StatusOK {
		t.Fatalf("首次 join 失败: status=%d err=%v", first.Status, first.Err)
	}

	// adopt 之前：二次 join 幂等，pet 仍为 null。
	second := joinStudent(h, "c1", "小明", "01")
	if second.Err != nil || second.Status != http.StatusOK {
		t.Fatalf("二次 join 失败: status=%d err=%v", second.Status, second.Err)
	}
	if second.Token == "" {
		t.Fatalf("二次 join 未返回 token")
	}
	if second.Token == first.Token {
		t.Errorf("二次 join 应签发新 token, 但与首次相同: %q", first.Token)
	}
	if petRaw := second.Body["pet"]; petRaw != nil {
		t.Errorf("adopt 前二次 join 的 pet 应为 null, 实际: %v", petRaw)
	}

	// 用首个 token 领养一只宠物。
	ids := eggIDs(t, h, first.Token)
	adopted := mustAdopt(t, h, first.Token, ids[0])

	// adopt 之后：三次 join 应返回其已有宠物，而不是第二只。
	third := joinStudent(h, "c1", "小明", "01")
	if third.Err != nil || third.Status != http.StatusOK {
		t.Fatalf("三次 join 失败: status=%d err=%v", third.Status, third.Err)
	}
	rePet := petFromAny(t, third.Body["pet"])
	if rePet.Species.ID != adopted.Species.ID {
		t.Errorf("三次 join 返回的宠物 species.id = %q, 与已领养的 %q 不一致", rePet.Species.ID, adopted.Species.ID)
	}

	// 新 token 身份同一：用它能看到同一只宠物。
	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", third.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("新 token GET /api/pet/me 状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
	}
	mePet := petFromAny(t, body["pet"])
	if mePet.Species.ID != adopted.Species.ID {
		t.Errorf("新 token 查询到的宠物 species.id = %q, 与已领养的 %q 不一致", mePet.Species.ID, adopted.Species.ID)
	}
}

// T4 蛋列表：恰好 6 颗，id 与颜色均唯一且非空。
func TestEggsListSixUniqueEggs(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c1", "小明", "01")

	resp, body := doJSON(t, h, http.MethodGet, "/api/eggs", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/eggs 状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
	}
	eggs, ok := body["eggs"].([]any)
	if !ok {
		t.Fatalf("/api/eggs 响应缺少 eggs 数组: %v", body)
	}
	if len(eggs) != 6 {
		t.Fatalf("蛋数量 = %d, 期望 6", len(eggs))
	}
	seenIDs := map[string]bool{}
	seenColors := map[string]bool{}
	for i, raw := range eggs {
		egg, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("第 %d 颗蛋不是 JSON 对象: %v", i, raw)
		}
		id, _ := egg["id"].(string)
		color, _ := egg["color"].(string)
		if id == "" {
			t.Errorf("第 %d 颗蛋 id 为空", i)
		}
		if color == "" {
			t.Errorf("第 %d 颗蛋 color 为空", i)
		}
		if seenIDs[id] {
			t.Errorf("蛋 id 重复: %q", id)
		}
		if seenColors[color] {
			t.Errorf("蛋颜色重复: %q", color)
		}
		seenIDs[id] = true
		seenColors[color] = true
	}
}

// T5 adopt 合法 eggId：返回字段齐全的新宠物（level=1、points=0、nextLevelPoints=20）。
func TestAdoptValidEggReturnsFullPet(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c1", "小明", "01")
	ids := eggIDs(t, h, token)

	p := mustAdopt(t, h, token, ids[1%len(ids)])
	checkPetShape(t, p)
	if p.Level != 1 {
		t.Errorf("pet.level = %d, 期望 1", p.Level)
	}
	if p.Points != 0 {
		t.Errorf("pet.points = %d, 期望 0", p.Points)
	}
	next := -1
	if p.NextLevelPoints != nil {
		next = *p.NextLevelPoints
	}
	if next != 20 {
		t.Errorf("pet.nextLevelPoints = %d, 期望 20", next)
	}
}

// T6 adopt eggId="random" 合法；不在蛋列表中的 eggId（含空串）→ 400。
func TestAdoptRandomEggAndInvalidEggId(t *testing.T) {
	h := newHandler(t)

	t.Run("random 合法", func(t *testing.T) {
		token := mustJoin(t, h, "c1", "小明", "01")
		p := mustAdopt(t, h, token, "random")
		checkPetShape(t, p)
	})

	t.Run("非法 eggId 返回 400", func(t *testing.T) {
		token := mustJoin(t, h, "c1", "小明", "02")
		for _, eggID := range []string{invalidEggID(t, h, token), ""} {
			status, raw := adoptEgg(h, token, eggID)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("eggId=%q 响应不是 JSON 对象: %s", eggID, raw)
			}
			expectError(t, status, body, http.StatusBadRequest, fmt.Sprintf("eggId=%q", eggID))
		}
	})
}

// T7 已有宠物的学生再 adopt → 409。
func TestAdoptConflictWhenPetExists(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c1", "小明", "01")
	ids := eggIDs(t, h, token)
	mustAdopt(t, h, token, ids[0])

	status, raw := adoptEgg(h, token, ids[1%len(ids)])
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("重复 adopt 响应不是 JSON 对象: %s", raw)
	}
	expectError(t, status, body, http.StatusConflict, "已有宠物再 adopt")
}

// T8 除 POST /api/join 外的 /api 均需 Bearer token：缺失或无效 → 401 + {"error":"..."}。
func TestAPIRequiresBearerToken(t *testing.T) {
	h := newHandler(t)

	targets := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/pet/me"},
		{http.MethodGet, "/api/eggs"},
		{http.MethodPost, "/api/adopt"},
	}
	for _, tg := range targets {
		t.Run(tg.method+" "+tg.path, func(t *testing.T) {
			var reqBody any
			if tg.method == http.MethodPost {
				reqBody = map[string]string{"eggId": "egg-1"}
			}
			resp, body := doJSON(t, h, tg.method, tg.path, "", reqBody)
			expectError(t, resp.StatusCode, body, http.StatusUnauthorized, tg.path+" 无 token")

			resp, body = doJSON(t, h, tg.method, tg.path, "not-a-valid-token", reqBody)
			expectError(t, resp.StatusCode, body, http.StatusUnauthorized, tg.path+" 坏 token")
		})
	}

	// 对照：join 本身不需要 token。
	res := joinStudent(h, "c1", "小明", "01")
	if res.Err != nil || res.Status != http.StatusOK {
		t.Errorf("join 不应要求 token: status=%d err=%v", res.Status, res.Err)
	}
}

// T9 /api/pet/me：M1 无加分，points=0、logSummary 必须是空数组 []。
func TestPetMeReturnsEmptyLogSummary(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c1", "小明", "01")
	mustAdopt(t, h, token, "random")

	resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pet/me 状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
	}
	p := petFromAny(t, body["pet"])
	checkPetShape(t, p)
	if p.Points != 0 {
		t.Errorf("pet.points = %d, 期望 0", p.Points)
	}
	if p.Level != 1 {
		t.Errorf("pet.level = %d, 期望 1", p.Level)
	}
	ls, ok := body["logSummary"].([]any)
	if !ok {
		t.Fatalf("logSummary 应为空数组 [], 实际: %v", body["logSummary"])
	}
	if len(ls) != 0 {
		t.Errorf("logSummary 长度 = %d, 期望 0", len(ls))
	}
}

// T10 改名：一次成功；二次 409；空白名/超长名（>24 字符）400；恰好 24 字符合法。
func TestRenamePetOnceThenConflict(t *testing.T) {
	h := newHandler(t)

	rename := func(t *testing.T, token, newName string) (*http.Response, map[string]any) {
		return doJSON(t, h, http.MethodPost, "/api/pet/name", token, map[string]string{"name": newName})
	}

	t.Run("改名一次成功", func(t *testing.T) {
		token := mustJoin(t, h, "c1", "甲", "01")
		mustAdopt(t, h, token, "random")
		resp, body := rename(t, token, "新名字")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("改名状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
		}
		p := petFromAny(t, body["pet"])
		if p.Name != "新名字" {
			t.Errorf("改名后 pet.name = %q, 期望 %q", p.Name, "新名字")
		}
	})

	t.Run("二次改名 409", func(t *testing.T) {
		token := mustJoin(t, h, "c1", "乙", "02")
		mustAdopt(t, h, token, "random")
		if resp, _ := rename(t, token, "第一次改名"); resp.StatusCode != http.StatusOK {
			t.Fatalf("首次改名未成功, 状态码 = %d", resp.StatusCode)
		}
		resp, body := rename(t, token, "第二次改名")
		expectError(t, resp.StatusCode, body, http.StatusConflict, "二次改名")
	})

	t.Run("空白名 400", func(t *testing.T) {
		token := mustJoin(t, h, "c1", "丙", "03")
		mustAdopt(t, h, token, "random")
		resp, body := rename(t, token, "   ")
		expectError(t, resp.StatusCode, body, http.StatusBadRequest, "空白名")
	})

	t.Run("超长名 400", func(t *testing.T) {
		token := mustJoin(t, h, "c1", "丁", "04")
		mustAdopt(t, h, token, "random")
		resp, body := rename(t, token, strings.Repeat("a", 25))
		expectError(t, resp.StatusCode, body, http.StatusBadRequest, "25 字符名")
	})

	t.Run("24 字符名合法", func(t *testing.T) {
		token := mustJoin(t, h, "c1", "戊", "05")
		mustAdopt(t, h, token, "random")
		want := strings.Repeat("a", 24)
		resp, body := rename(t, token, want)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("24 字符改名状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
		}
		p := petFromAny(t, body["pet"])
		if p.Name != want {
			t.Errorf("改名后 pet.name = %q, 期望 %q", p.Name, want)
		}
	})
}
