package server_test

// M2 积分流流水黑盒用例（T8 分页 / T9 不可改删 / T14 page 溢出回灌）。

import (
	"net/http"
	"testing"
)

// T8 流水分页：pageSize 固定 20、id 倒序、缺省 page=1、page<1 或非数字 → 400。
// 命令: go test ./server/ -run TestLog_Pagination -v
func TestLog_Pagination(t *testing.T) {
	h := newHandler(t)
	token, _ := m2JoinedToken(t, h)

	m2SeedPoints(t, h, token, 25)

	// page=1：恰 20 条、total=25、pageSize=20、按 id 倒序（新流水在前）
	status, list, raw := m2GetLog(h, token, "?page=1")
	if status != http.StatusOK {
		t.Fatalf("page=1 状态码 = %d, 期望 200; body=%s", status, raw)
	}
	if list.Page != 1 || list.PageSize != 20 || list.Total != 25 {
		t.Fatalf("page=1 分页元信息 = (page %d, pageSize %d, total %d), 期望 (1, 20, 25)",
			list.Page, list.PageSize, list.Total)
	}
	if len(list.Items) != 20 {
		t.Fatalf("page=1 返回 %d 条, 期望 20", len(list.Items))
	}
	for i, item := range list.Items {
		wantID := int64(25 - i) // 25, 24, ..., 6
		if item.ID != wantID {
			t.Errorf("page=1 items[%d].id = %d, 期望 %d（应按 id 倒序）", i, item.ID, wantID)
		}
		if item.CreatedAt == "" {
			t.Errorf("items[%d].createdAt 为空, 期望非空", i)
		}
		if item.Value < 1 || item.Value > 10 {
			t.Errorf("items[%d].value = %d, 期望在 1..10", i, item.Value)
		}
		if item.Reason == "" {
			t.Errorf("items[%d].reason 为空, 期望非空", i)
		}
	}

	// page=2：剩余 5 条（id 5..1）
	status, list, raw = m2GetLog(h, token, "?page=2")
	if status != http.StatusOK {
		t.Fatalf("page=2 状态码 = %d, 期望 200; body=%s", status, raw)
	}
	if list.Page != 2 || list.Total != 25 || len(list.Items) != 5 {
		t.Fatalf("page=2 = (page %d, total %d, %d 条), 期望 (2, 25, 5)", list.Page, list.Total, len(list.Items))
	}
	if list.Items[0].ID != 5 || list.Items[4].ID != 1 {
		t.Errorf("page=2 首尾 id = (%d, %d), 期望 (5, 1)", list.Items[0].ID, list.Items[4].ID)
	}

	// 缺省 page 等价于 page=1
	status, list, raw = m2GetLog(h, token, "")
	if status != http.StatusOK {
		t.Fatalf("缺省 page 状态码 = %d, 期望 200; body=%s", status, raw)
	}
	if list.Page != 1 || len(list.Items) != 20 || list.Total != 25 {
		t.Errorf("缺省 page = (page %d, %d 条, total %d), 期望 (1, 20, 25)",
			list.Page, len(list.Items), list.Total)
	}

	// page 越过末页：返回空列表
	status, list, raw = m2GetLog(h, token, "?page=3")
	if status != http.StatusOK {
		t.Fatalf("page=3 状态码 = %d, 期望 200; body=%s", status, raw)
	}
	if len(list.Items) != 0 || list.Total != 25 {
		t.Errorf("page=3 = (%d 条, total %d), 期望 (0, 25)", len(list.Items), list.Total)
	}

	// 非法 page：0、-1、abc 一律 400
	for _, p := range []string{"0", "-1", "abc"} {
		status, _, raw = m2GetLog(h, token, "?page="+p)
		if status != http.StatusBadRequest {
			t.Errorf("page=%s 状态码 = %d, 期望 400; body=%s", p, status, raw)
		}
	}
}

// T9 流水不可改删：/api/points 仅接受 POST，流水端点不提供修改/删除方法。
// 命令: go test ./server/ -run TestImmutability_MethodNotAllowed -v
func TestImmutability_MethodNotAllowed(t *testing.T) {
	h := newHandler(t)
	token, _ := m2JoinedToken(t, h)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if status := m2Status(h, method, "/api/points", token, nil); status != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/points 状态码 = %d, 期望 405", method, status)
		}
	}
	if status := m2Status(h, http.MethodDelete, "/api/pet/me/log", token, nil); status != http.StatusMethodNotAllowed {
		t.Error("DELETE /api/pet/me/log 状态码非 405, 期望 405（流水不可删）")
	}
	if status := m2Status(h, http.MethodPut, "/api/pet/me/log", token, nil); status != http.StatusMethodNotAllowed {
		t.Error("PUT /api/pet/me/log 状态码非 405, 期望 405")
	}
}

// T14 page 溢出回灌用例（对抗审查发现）：超大 page 的 (page-1)*pageSize 会整型溢出，
// SQLite 把负 OFFSET 当 0，曾导致回退返回第一页数据；修复后须拦截或返回空列表。
// 命令: go test ./server/ -run TestLog_PageOverflow_NotReturnFirstPage -v
func TestLog_PageOverflow_NotReturnFirstPage(t *testing.T) {
	h := newHandler(t)
	token, _ := m2JoinedToken(t, h)
	m2SeedPoints(t, h, token, 5)

	status, list, raw := m2GetLog(h, token, "?page=9223372036854775807")
	if status != http.StatusOK && status != http.StatusBadRequest {
		t.Fatalf("超大 page 状态码 = %d, 期望 200 或 400; body=%s", status, raw)
	}
	if status == http.StatusOK && len(list.Items) != 0 {
		t.Errorf("超大 page 返回 %d 条数据, 期望空列表（不得回退第一页）; body=%s", len(list.Items), raw)
	}
}
