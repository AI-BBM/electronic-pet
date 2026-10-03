package server_test

// M3 图鉴与班级墙黑盒用例（M3-T1 … M3-T12；M3-T13 为手动回归命令，见文件尾注释）。
// 契约来源：docs/product/features/m3-dex-wall.md + M3 测试先行任务书。
//
// 反随机原则：adopt 的种类由服务端按稀有度加权随机，所有断言以
// 「adopt 响应返回的 speciesId / petId」为锚点做确定性交叉断言，
// 鸽笼原理（13 宠 12 种必有重种）用于产生无随机的拥有数下界。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// M3-T1 dex 鉴权：无 token / 篡改 token → 401 + 非空 error。
// 命令: go test ./server/ -run TestM3_DexRequiresToken -v
func TestM3_DexRequiresToken(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c301", "张三", "S001")
	m3ExpectUnauthorized(t, h, "/api/dex", token)
}

// M3-T2 新班级（join 未 adopt）dex 全锁：恰好 12 条、id/rarity/silhouette 齐全、
// 全部 unlocked=false / owners=0 / stages=null，且序列化后不出现 "stages":[。
// 命令: go test ./server/ -run TestM3_DexAllLockedForFreshClass -v
func TestM3_DexAllLockedForFreshClass(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c302", "李四", "S001")

	status, resp, raw := m3GetDex(t, h, token)
	if status != http.StatusOK {
		t.Fatalf("GET /api/dex 状态码 = %d, 期望 200", status)
	}
	m3CheckDexShape(t, resp)

	for _, entry := range m3DexEntryMaps(t, raw) {
		m3CheckLockedEntry(t, entry)
	}
}

// M3-T3 孵出即解锁：本班 1 人 adopt 后，恰好该 speciesId 条目 unlocked=true、
// owners=1、stages 长度 3 且全部非空；其余 11 条仍 locked（stages=null，红线）。
// 命令: go test ./server/ -run TestM3_DexUnlockedAfterAdopt -v
func TestM3_DexUnlockedAfterAdopt(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c303", "张小测", "S001")
	adopted := m3Adopt(t, h, token)

	status, resp, raw := m3GetDex(t, h, token)
	if status != http.StatusOK {
		t.Fatalf("GET /api/dex 状态码 = %d, 期望 200", status)
	}
	m3CheckDexShape(t, resp)

	unlockedCount := 0
	for _, e := range resp.Species {
		if e.ID == adopted.Species.ID {
			m3CheckUnlockedEntry(t, e)
			if e.Owners != 1 {
				t.Errorf("dex[%s]: owners = %d, 期望 1（本班恰好 1 人孵出）", e.ID, e.Owners)
			}
			unlockedCount++
		}
	}
	if unlockedCount != 1 {
		t.Errorf("解锁条数 = %d, 期望恰好 1（只有 adopt 的那种）", unlockedCount)
	}

	// 其余 11 条 locked：map 语义红线检查（stages 键存在且为 null）
	entryMaps := m3DexEntryMaps(t, raw)
	lockedSeen := 0
	for _, entry := range entryMaps {
		id, _ := entry["id"].(string)
		if id == adopted.Species.ID {
			continue
		}
		m3CheckLockedEntry(t, entry)
		lockedSeen++
	}
	if lockedSeen != 11 {
		t.Errorf("locked 条数 = %d, 期望 11", lockedSeen)
	}
}

// M3-T4 班级隔离：c1/c2 各 6 人孵出后交叉断言——一班独有种类在二班 dex 仍 locked
// （反之亦然）；c3 只 join 不 adopt 的班级 dex 全锁；一班 wall 只含一班学生。
// 随机器可能撞种，故用「两班种类集合差」做断言面，集合差为空时显式 Fatal 提示重跑。
// 命令: go test ./server/ -run TestM3_DexClassIsolation -v
func TestM3_DexClassIsolation(t *testing.T) {
	h := newHandler(t)
	const n = 6

	var toks1, toks2 []string
	names1 := map[string]bool{}
	names2 := map[string]bool{}
	set1 := map[string]bool{}
	set2 := map[string]bool{}
	for i := 0; i < n; i++ {
		name1 := fmt.Sprintf("一班%02d", i+1)
		t1 := mustJoin(t, h, "c1", name1, fmt.Sprintf("S%02d", i+1))
		p1 := m3Adopt(t, h, t1)
		toks1 = append(toks1, t1)
		names1[name1] = true
		set1[p1.Species.ID] = true

		name2 := fmt.Sprintf("二班%02d", i+1)
		t2 := mustJoin(t, h, "c2", name2, fmt.Sprintf("S%02d", i+1))
		p2 := m3Adopt(t, h, t2)
		toks2 = append(toks2, t2)
		names2[name2] = true
		set2[p2.Species.ID] = true
	}

	onlyIn1 := map[string]bool{}
	onlyIn2 := map[string]bool{}
	for id := range set1 {
		if !set2[id] {
			onlyIn1[id] = true
		}
	}
	for id := range set2 {
		if !set1[id] {
			onlyIn2[id] = true
		}
	}
	if len(onlyIn1) == 0 || len(onlyIn2) == 0 {
		t.Fatalf("随机孵出未形成两班差异集合（一班种类=%v 二班种类=%v），无法做隔离断言，请重跑本用例",
			set1, set2)
	}

	// c3：只 join 不 adopt → 全部 12 条 locked（别的班孵出不算解锁）
	tok3 := mustJoin(t, h, "c3", "三班独苗", "S001")
	status3, resp3, raw3 := m3GetDex(t, h, tok3)
	if status3 != http.StatusOK {
		t.Fatalf("c3 GET /api/dex 状态码 = %d, 期望 200", status3)
	}
	m3CheckDexShape(t, resp3)
	for _, entry := range m3DexEntryMaps(t, raw3) {
		m3CheckLockedEntry(t, entry)
	}

	// 一班独有种类在二班 dex 必须 locked；二班独有种类在一班 dex 必须 locked
	map2 := map[string]map[string]any{}
	for _, entry := range m3DexEntryMaps(t, mustDexRaw(t, h, toks2[0])) {
		id, _ := entry["id"].(string)
		map2[id] = entry
	}
	for id := range onlyIn1 {
		entry, ok := map2[id]
		if !ok {
			t.Errorf("二班 dex 缺少种类 %s", id)
			continue
		}
		m3CheckLockedEntry(t, entry)
	}
	map1 := map[string]map[string]any{}
	for _, entry := range m3DexEntryMaps(t, mustDexRaw(t, h, toks1[0])) {
		id, _ := entry["id"].(string)
		map1[id] = entry
	}
	for id := range onlyIn2 {
		entry, ok := map1[id]
		if !ok {
			t.Errorf("一班 dex 缺少种类 %s", id)
			continue
		}
		m3CheckLockedEntry(t, entry)
	}

	// wall 隔离：一班墙恰好 6 条、全部是一班学生、种类都在一班孵出集合内
	statusW, wall1, _ := m3GetWall(t, h, toks1[0], "")
	if statusW != http.StatusOK {
		t.Fatalf("一班 GET /api/class/wall 状态码 = %d, 期望 200", statusW)
	}
	if len(wall1.Wall) != n {
		t.Errorf("一班 wall 条数 = %d, 期望 %d（不得混入他班）", len(wall1.Wall), n)
	}
	for i, e := range wall1.Wall {
		if !names1[e.StudentName] {
			t.Errorf("一班 wall 第 %d 条 studentName = %q, 属于他班学生", i, e.StudentName)
		}
		if !set1[e.SpeciesID] {
			t.Errorf("一班 wall 第 %d 条 speciesId = %q, 不在一班孵出集合内", i, e.SpeciesID)
		}
	}
}

// mustDexRaw 拉取 /api/dex 原始 body（T4 内部小工具，避免重复样板）。
func mustDexRaw(t *testing.T, h http.Handler, token string) []byte {
	t.Helper()
	status, _, raw := m3GetDex(t, h, token)
	if status != http.StatusOK {
		t.Fatalf("GET /api/dex 状态码 = %d, 期望 200", status)
	}
	return raw
}

// M3-T5 鸽笼拥有数：同班 13 人各 adopt（eggId=random），
// dex 的 owners 逐种与 adopt 响应统计精确一致，sum(owners)=13 且 max(owners)>=2；
// 附带 wall 同分并列 tie-break：13 人全是 0 分，sort=points 须按 petId 升序稳定。
// 命令: go test ./server/ -run TestM3_DexOwnersPigeonhole -v
func TestM3_DexOwnersPigeonhole(t *testing.T) {
	h := newHandler(t)
	const n = 13

	expectOwners := map[string]int{}
	var lastToken string
	for i := 1; i <= n; i++ {
		lastToken = mustJoin(t, h, "c305", fmt.Sprintf("同学%02d", i), fmt.Sprintf("S%02d", i))
		p := m3Adopt(t, h, lastToken)
		expectOwners[p.Species.ID]++
	}

	status, resp, raw := m3GetDex(t, h, lastToken)
	if status != http.StatusOK {
		t.Fatalf("GET /api/dex 状态码 = %d, 期望 200", status)
	}
	m3CheckDexShape(t, resp)

	entryMaps := map[string]map[string]any{}
	for _, entry := range m3DexEntryMaps(t, raw) {
		id, _ := entry["id"].(string)
		entryMaps[id] = entry
	}

	sum, maxOwners := 0, 0
	for _, e := range resp.Species {
		if want := expectOwners[e.ID]; e.Owners != want {
			t.Errorf("dex[%s]: owners = %d, 期望 %d（按 adopt 响应统计）", e.ID, e.Owners, want)
		}
		sum += e.Owners
		if e.Owners > maxOwners {
			maxOwners = e.Owners
		}
		if e.Owners > 0 {
			m3CheckUnlockedEntry(t, e)
		} else {
			m3CheckLockedEntry(t, entryMaps[e.ID])
		}
	}
	if sum != n {
		t.Errorf("sum(owners) = %d, 期望 %d", sum, n)
	}
	if maxOwners < 2 {
		t.Errorf("max(owners) = %d, 期望 >= 2（%d 宠 %d 种鸽笼）", maxOwners, n, 12)
	}

	// 同分 tie-break：全班 0 分，sort=points 须按 petId 升序
	statusW, wall, _ := m3GetWall(t, h, lastToken, "?sort=points")
	if statusW != http.StatusOK {
		t.Fatalf("GET /api/class/wall?sort=points 状态码 = %d, 期望 200", statusW)
	}
	if len(wall.Wall) != n {
		t.Fatalf("wall 条数 = %d, 期望 %d（每人恰好一条）", len(wall.Wall), n)
	}
	seenNames := map[string]bool{}
	for i, e := range wall.Wall {
		if seenNames[e.StudentName] {
			t.Errorf("wall 中 %q 出现多次（每人应恰好一条）", e.StudentName)
		}
		seenNames[e.StudentName] = true
		if e.Points != 0 {
			t.Errorf("wall[%d]（%s）: points = %d, 期望 0（尚未加分）", i, e.StudentName, e.Points)
		}
		if i > 0 && wall.Wall[i-1].PetID >= e.PetID {
			t.Errorf("同分并列未按 petId 升序稳定: wall[%d].petId=%d >= wall[%d].petId=%d",
				i-1, wall.Wall[i-1].PetID, i, e.PetID)
		}
	}
}

// M3-T6 wall 鉴权：无 token / 篡改 token → 401 + 非空 error（缺省与 recent 两种 query）。
// 命令: go test ./server/ -run TestM3_WallRequiresToken -v
func TestM3_WallRequiresToken(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c306", "王五", "S001")
	m3ExpectUnauthorized(t, h, "/api/class/wall", token)
	m3ExpectUnauthorized(t, h, "/api/class/wall?sort=recent", token)
}

// M3-T7 空班与 sort 参数：空班 wall 为 []（数组非 null）且缺省 sort=points；
// 非法 sort 值 → 400 + 非空 error；显式 sort=recent → 200 且 body.sort=="recent"。
// 命令: go test ./server/ -run TestM3_WallEmptyClassAndSortParam -v
func TestM3_WallEmptyClassAndSortParam(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c307", "赵六", "S001")

	// 空班：wall 必须是空数组（非 null、非缺键），缺省 sort=points
	status, resp, raw := m3GetWall(t, h, token, "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/class/wall 状态码 = %d, 期望 200", status)
	}
	if resp.Sort != "points" {
		t.Errorf("缺省 body.sort = %q, 期望 \"points\"", resp.Sort)
	}
	if body := m3DecodeJSON(raw); body != nil {
		if arr, ok := body["wall"].([]any); !ok {
			t.Errorf("空班 wall = %v, 期望空数组 []（不得为 null/缺键）", body["wall"])
		} else if len(arr) != 0 {
			t.Errorf("空班 wall 长度 = %d, 期望 0", len(arr))
		}
	}
	if len(resp.Wall) != 0 {
		t.Errorf("空班 wall 条数 = %d, 期望 0", len(resp.Wall))
	}

	// 非法 sort → 400
	statusBad, _, rawBad := m3GetWall(t, h, token, "?sort=bogus")
	expectError(t, statusBad, m3DecodeJSON(rawBad), http.StatusBadRequest, "GET /api/class/wall?sort=bogus")

	// 显式 sort=recent → 200 且回显 recent
	statusR, respR, _ := m3GetWall(t, h, token, "?sort=recent")
	if statusR != http.StatusOK {
		t.Fatalf("GET /api/class/wall?sort=recent 状态码 = %d, 期望 200（body=%s）", statusR, rawBad)
	}
	if respR.Sort != "recent" {
		t.Errorf("body.sort = %q, 期望 \"recent\"", respR.Sort)
	}
	if len(respR.Wall) != 0 {
		t.Errorf("空班 recent wall 条数 = %d, 期望 0", len(respR.Wall))
	}
}

// M3-T8 单人 wall 字段：恰好 1 条，petId/petName/speciesId/level=1/points=0 与
// adopt 结果一致，studentName 为 join 姓名，imageUrl 非空，
// latestReason 显式 ""、latestActivity 显式 null，条目无 studentNo 键。
// 命令: go test ./server/ -run TestM3_WallSingleEntryFields -v
func TestM3_WallSingleEntryFields(t *testing.T) {
	h := newHandler(t)
	const studentName = "张小测"
	token := mustJoin(t, h, "c308", studentName, "S001")
	adopted := m3Adopt(t, h, token)

	status, resp, raw := m3GetWall(t, h, token, "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/class/wall 状态码 = %d, 期望 200", status)
	}
	if len(resp.Wall) != 1 {
		t.Fatalf("wall 条数 = %d, 期望 1（仅已孵出的学生上墙）", len(resp.Wall))
	}
	e := resp.Wall[0]
	if e.PetID != adopted.ID {
		t.Errorf("petId = %d, 期望 adopt 返回的 %d", e.PetID, adopted.ID)
	}
	if e.PetName != adopted.Name {
		t.Errorf("petName = %q, 期望 adopt 返回的 %q", e.PetName, adopted.Name)
	}
	if e.SpeciesID != adopted.Species.ID {
		t.Errorf("speciesId = %q, 期望 adopt 返回的 %q", e.SpeciesID, adopted.Species.ID)
	}
	if e.SpeciesName != adopted.Species.Name {
		t.Errorf("speciesName = %q, 期望 adopt 返回的 %q", e.SpeciesName, adopted.Species.Name)
	}
	if e.Rarity != adopted.Species.Rarity || !isValidRarity(e.Rarity) {
		t.Errorf("rarity = %q, 期望与 adopt 一致且合法（%q）", e.Rarity, adopted.Species.Rarity)
	}
	if e.StudentName != studentName {
		t.Errorf("studentName = %q, 期望 join 姓名 %q", e.StudentName, studentName)
	}
	if e.Level != 1 || adopted.Level != 1 {
		t.Errorf("level = %d / adopt level = %d, 期望均为 1", e.Level, adopted.Level)
	}
	if e.Points != 0 || adopted.Points != 0 {
		t.Errorf("points = %d / adopt points = %d, 期望均为 0", e.Points, adopted.Points)
	}
	if e.ImageURL == "" {
		t.Errorf("imageUrl 为空, 期望当前阶段（Lv1）立绘 URL")
	}
	if e.LatestReason != "" {
		t.Errorf("latestReason = %q, 期望 \"\"（尚无流水）", e.LatestReason)
	}
	if e.LatestActivity != nil {
		t.Errorf("latestActivity = %v, 期望 null（尚无流水）", *e.LatestActivity)
	}

	// 键级检查：latestReason/latestActivity 显式存在、无 studentNo
	entries := m3WallEntryMaps(t, raw)
	if len(entries) != 1 {
		t.Fatalf("wall map 条数 = %d, 期望 1", len(entries))
	}
	m3CheckWallEntryKeys(t, entries[0], "wall[0]")
}

// M3-T9 points 排序：3 人分别加到 25/5/60 分（10/5/1 组合），sort=points 后
// points 严格降序、首位是 60 分者、逐人分值吻合；缺省排序与显式 points 同序。
// 命令: go test ./server/ -run TestM3_WallSortByPoints -v
func TestM3_WallSortByPoints(t *testing.T) {
	h := newHandler(t)
	targets := []struct {
		name  string
		total int
	}{
		{"甲二十五", 25},
		{"乙五", 5},
		{"丙六十", 60},
	}

	tokens := map[string]string{}
	for i, tg := range targets {
		token := mustJoin(t, h, "c309", tg.name, fmt.Sprintf("S%02d", i+1))
		m3Adopt(t, h, token)
		var combo []int
		switch tg.total {
		case 25:
			combo = []int{10, 10, 5}
		case 5:
			combo = []int{5}
		case 60:
			combo = []int{10, 10, 10, 10, 10, 10}
		default:
			t.Fatalf("未定义 %d 分的组合", tg.total)
		}
		for j, v := range combo {
			res := m2AddPoints(h, token, m2PointsRequest{
				Reason: "课堂表现", Value: v, RequestID: fmt.Sprintf("t9-%s-%d", tg.name, j),
			})
			if res.Status != http.StatusOK || !res.Added {
				t.Fatalf("%s 第 %d 次加分失败: status=%d added=%v body=%v", tg.name, j+1, res.Status, res.Added, res.Body)
			}
		}
		tokens[tg.name] = token
	}

	status, resp, _ := m3GetWall(t, h, tokens["甲二十五"], "?sort=points")
	if status != http.StatusOK {
		t.Fatalf("GET /api/class/wall?sort=points 状态码 = %d, 期望 200", status)
	}
	if len(resp.Wall) != len(targets) {
		t.Fatalf("wall 条数 = %d, 期望 %d", len(resp.Wall), len(targets))
	}
	wantOrder := []string{"丙六十", "甲二十五", "乙五"}
	wantPoints := []int{60, 25, 5}
	for i := 1; i < len(resp.Wall); i++ {
		if resp.Wall[i-1].Points <= resp.Wall[i].Points {
			t.Errorf("points 未严格降序: wall[%d]=%d <= wall[%d]=%d",
				i-1, resp.Wall[i-1].Points, i, resp.Wall[i].Points)
		}
	}
	for i, e := range resp.Wall {
		if e.StudentName != wantOrder[i] {
			t.Errorf("wall[%d].studentName = %q, 期望 %q（按积分排行）", i, e.StudentName, wantOrder[i])
		}
		if e.Points != wantPoints[i] {
			t.Errorf("wall[%d]（%s）: points = %d, 期望 %d", i, e.StudentName, e.Points, wantPoints[i])
		}
	}

	// 缺省 sort：同 points 序且 body.sort 回显 points
	statusD, respD, _ := m3GetWall(t, h, tokens["甲二十五"], "")
	if statusD != http.StatusOK {
		t.Fatalf("GET /api/class/wall（缺省）状态码 = %d, 期望 200", statusD)
	}
	if respD.Sort != "points" {
		t.Errorf("缺省 body.sort = %q, 期望 \"points\"", respD.Sort)
	}
	if len(respD.Wall) != len(wantOrder) {
		t.Fatalf("缺省 wall 条数 = %d, 期望 %d", len(respD.Wall), len(wantOrder))
	}
	for i := range respD.Wall {
		if respD.Wall[i].StudentName != wantOrder[i] {
			t.Errorf("缺省排序 wall[%d].studentName = %q, 期望 %q（缺省即 points）", i, respD.Wall[i].StudentName, wantOrder[i])
		}
	}
}

// M3-T10 recent 排序：A 先加分、B 后加分（B 的流水自增 id 更大）→ sort=recent
// 首位 B、次位 A；再让 C、D 孵出但无流水 → 排在最后且按 petId 升序；
// A/B 的 latestReason/latestActivity 取自各自最新流水，C/D 为空/null。
// 命令: go test ./server/ -run TestM3_WallSortByRecent -v
func TestM3_WallSortByRecent(t *testing.T) {
	h := newHandler(t)

	tokA := mustJoin(t, h, "c310", "张甲", "S001")
	m3Adopt(t, h, tokA)
	if res := m2AddPoints(h, tokA, m2PointsRequest{Reason: "课堂表现", Value: 1, RequestID: "t10-a"}); res.Status != http.StatusOK || !res.Added {
		t.Fatalf("张甲加分失败: %v", res.Body)
	}

	tokB := mustJoin(t, h, "c310", "张乙", "S002")
	m3Adopt(t, h, tokB)
	if res := m2AddPoints(h, tokB, m2PointsRequest{Reason: "作业优秀", Value: 1, RequestID: "t10-b"}); res.Status != http.StatusOK || !res.Added {
		t.Fatalf("张乙加分失败: %v", res.Body)
	}

	tokC := mustJoin(t, h, "c310", "张丙", "S003")
	petC := m3Adopt(t, h, tokC)
	tokD := mustJoin(t, h, "c310", "张丁", "S004")
	petD := m3Adopt(t, h, tokD)

	status, resp, _ := m3GetWall(t, h, tokA, "?sort=recent")
	if status != http.StatusOK {
		t.Fatalf("GET /api/class/wall?sort=recent 状态码 = %d, 期望 200", status)
	}
	if resp.Sort != "recent" {
		t.Errorf("body.sort = %q, 期望 \"recent\"", resp.Sort)
	}
	if len(resp.Wall) != 4 {
		t.Fatalf("wall 条数 = %d, 期望 4", len(resp.Wall))
	}

	want := []string{"张乙", "张甲", "张丙", "张丁"}
	for i, e := range resp.Wall {
		if e.StudentName != want[i] {
			t.Errorf("recent 排序 wall[%d].studentName = %q, 期望 %q", i, e.StudentName, want[i])
		}
	}
	if resp.Wall[0].LatestReason != "作业优秀" {
		t.Errorf("wall[0].latestReason = %q, 期望 张乙最新流水的 \"作业优秀\"", resp.Wall[0].LatestReason)
	}
	if resp.Wall[1].LatestReason != "课堂表现" {
		t.Errorf("wall[1].latestReason = %q, 期望 张甲最新流水的 \"课堂表现\"", resp.Wall[1].LatestReason)
	}
	for i := 0; i < 2; i++ {
		if resp.Wall[i].LatestActivity == nil || *resp.Wall[i].LatestActivity == "" {
			t.Errorf("wall[%d].latestActivity = %v, 期望非空时间串（有流水）", i, resp.Wall[i].LatestActivity)
		}
	}
	for i := 2; i < 4; i++ {
		if resp.Wall[i].LatestReason != "" {
			t.Errorf("wall[%d].latestReason = %q, 期望 \"\"（无流水）", i, resp.Wall[i].LatestReason)
		}
		if resp.Wall[i].LatestActivity != nil {
			t.Errorf("wall[%d].latestActivity = %v, 期望 null（无流水）", i, *resp.Wall[i].LatestActivity)
		}
	}
	// 无流水并列按 petId 升序稳定（张丙先孵出，petId 更小）
	if petC.ID >= petD.ID {
		t.Fatalf("前置校验失败：张丙 petId=%d 应小于 张丁 petId=%d（自增 id）", petC.ID, petD.ID)
	}
	if resp.Wall[2].PetID >= resp.Wall[3].PetID {
		t.Errorf("无流水并列未按 petId 升序: wall[2].petId=%d >= wall[3].petId=%d",
			resp.Wall[2].PetID, resp.Wall[3].PetID)
	}
}

// M3-T11 实时性：每轮加分后立刻查 wall 与 GET /api/pet/me，
// 两处 points/level 必须一致且等于累计值（第二轮跨过 Lv2 阈值）。
// 命令: go test ./server/ -run TestM3_WallReflectsPointsImmediately -v
func TestM3_WallReflectsPointsImmediately(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c311", "实时妹", "S001")
	m3Adopt(t, h, token)

	rounds := [][]int{{5, 2}, {10, 10}} // 累计 7 → 27（第二轮跨过 Lv2=20）
	total := 0
	for ri, round := range rounds {
		for j, v := range round {
			res := m2AddPoints(h, token, m2PointsRequest{
				Reason: "作业优秀", Value: v, RequestID: fmt.Sprintf("t11-%d-%d", ri, j),
			})
			if res.Status != http.StatusOK || !res.Added {
				t.Fatalf("第 %d 轮第 %d 次加分失败: %v", ri+1, j+1, res.Body)
			}
			total += v
		}

		statusW, wall, _ := m3GetWall(t, h, token, "")
		if statusW != http.StatusOK {
			t.Fatalf("第 %d 轮 GET /api/class/wall 状态码 = %d, 期望 200", ri+1, statusW)
		}
		if len(wall.Wall) != 1 {
			t.Fatalf("第 %d 轮 wall 条数 = %d, 期望 1", ri+1, len(wall.Wall))
		}
		we := wall.Wall[0]

		resp, body := doJSON(t, h, http.MethodGet, "/api/pet/me", token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 轮 GET /api/pet/me 状态码 = %d, 期望 200", ri+1, resp.StatusCode)
		}
		me := petFromAny(t, body["pet"])

		if we.Points != me.Points {
			t.Errorf("第 %d 轮: wall points = %d, /api/pet/me points = %d, 期望一致（排行实时反映加分）",
				ri+1, we.Points, me.Points)
		}
		if we.Points != total {
			t.Errorf("第 %d 轮: wall points = %d, 期望累计 %d", ri+1, we.Points, total)
		}
		if we.Level != me.Level {
			t.Errorf("第 %d 轮: wall level = %d, /api/pet/me level = %d, 期望一致", ri+1, we.Level, me.Level)
		}
	}
}

// M3-T12 静态契约：GET / 返回的 index.html 必须包含 data-view="dex" 与
// data-view="wall" 两个新视图标记，且 M1 的四个标记（join/eggs/hatch/pet）仍在。
// 命令: go test ./server/ -run TestM3_IndexHasDexAndWallViews -v
func TestM3_IndexHasDexAndWallViews(t *testing.T) {
	h := newHandler(t)
	status, raw := m3Get(h, "/", "")
	if status != http.StatusOK {
		t.Fatalf("GET / 状态码 = %d, 期望 200", status)
	}
	html := string(raw)
	for _, marker := range []string{
		`data-view="join"`, `data-view="eggs"`, `data-view="hatch"`, `data-view="pet"`,
		`data-view="dex"`, `data-view="wall"`,
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("/ 返回的 index.html 缺少视图标记 %s", marker)
		}
	}
}

// M3-T13 回归（手动命令，非自动化用例）：
//   1. go test ./...            —— 全绿（M1/M2 既有用例无回归，M3 新用例转绿）
//   2. go vet ./...             —— 通过
//   3. git diff origin/main --name-status —— 测试阶段只增不改不删：
//      本阶段基线为空 diff；新增文件仅 server/m3_helpers_test.go 与 server/dex_wall_test.go
//      （均为 untracked 新文件，git status --porcelain 应只出现 ?? 条目）。
//      实现阶段落地后复核：既有文件可按需修改（实现需要改 server.go/index.html 等），
//      但不得删除/改写本目录既有测试文件与其断言。
