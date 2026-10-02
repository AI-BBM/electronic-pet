package server

import (
	"net/http"
	"testing"
)

// logItemJSON 是流水中单条记录的形状。
type logItemJSON struct {
	ID        int64  `json:"id"`
	Value     int    `json:"value"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
}

// logListJSON 是 GET /api/pet/me/log 的响应体形状。
type logListJSON struct {
	Items    []logItemJSON `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
	Total    int           `json:"total"`
}

// T8 流水分页：pageSize 固定 20、id 倒序、缺省 page=1、page<1 或非数字 → 400。
// 命令: go test ./server/ -run TestLog_Pagination -v
func TestLog_Pagination(t *testing.T) {
	env := newTestEnv(t)
	db := env.store.DB()
	studentID, petID := seedStudentWithPet(t, db, 1, 0)
	seedLogs(t, db, petID, 25)
	token := env.token(studentID)

	get := func(query string) (int, logListJSON, []byte) {
		status, body := env.do(t, http.MethodGet, "/api/pet/me/log"+query, token, nil)
		return status, decodeLogList(t, body), body
	}

	// page=1：恰 20 条、total=25、pageSize=20、按 id 倒序（新流水在前）
	status, lr, body := get("?page=1")
	if status != http.StatusOK {
		t.Fatalf("page=1 状态码 = %d, 期望 200; body=%s", status, body)
	}
	if lr.Page != 1 || lr.PageSize != 20 || lr.Total != 25 {
		t.Fatalf("page=1 分页元信息 = (page %d, pageSize %d, total %d), 期望 (1, 20, 25)",
			lr.Page, lr.PageSize, lr.Total)
	}
	if len(lr.Items) != 20 {
		t.Fatalf("page=1 返回 %d 条, 期望 20", len(lr.Items))
	}
	for i, item := range lr.Items {
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
	status, lr, body = get("?page=2")
	if status != http.StatusOK {
		t.Fatalf("page=2 状态码 = %d, 期望 200; body=%s", status, body)
	}
	if lr.Page != 2 || lr.Total != 25 || len(lr.Items) != 5 {
		t.Fatalf("page=2 = (page %d, total %d, %d 条), 期望 (2, 25, 5)", lr.Page, lr.Total, len(lr.Items))
	}
	if lr.Items[0].ID != 5 || lr.Items[4].ID != 1 {
		t.Errorf("page=2 首尾 id = (%d, %d), 期望 (5, 1)", lr.Items[0].ID, lr.Items[4].ID)
	}

	// 缺省 page 等价于 page=1
	status, lr, body = get("")
	if status != http.StatusOK {
		t.Fatalf("缺省 page 状态码 = %d, 期望 200; body=%s", status, body)
	}
	if lr.Page != 1 || len(lr.Items) != 20 || lr.Total != 25 {
		t.Errorf("缺省 page = (page %d, %d 条, total %d), 期望 (1, 20, 25)",
			lr.Page, len(lr.Items), lr.Total)
	}

	// page 越过末页：契约只规定 page<1 或非数字才 400，越界页应返回空列表
	status, lr, body = get("?page=3")
	if status != http.StatusOK {
		t.Fatalf("page=3 状态码 = %d, 期望 200; body=%s", status, body)
	}
	if len(lr.Items) != 0 || lr.Total != 25 {
		t.Errorf("page=3 = (%d 条, total %d), 期望 (0, 25)", len(lr.Items), lr.Total)
	}

	// 非法 page：0、-1、abc 一律 400
	for _, p := range []string{"0", "-1", "abc"} {
		status, _, body = get("?page=" + p)
		if status != http.StatusBadRequest {
			t.Errorf("page=%s 状态码 = %d, 期望 400; body=%s", p, status, body)
		}
	}
}

// T9 流水不可改删：/api/points 仅接受 POST，流水端点不提供修改/删除方法。
// 命令: go test ./server/ -run TestImmutability_MethodNotAllowed -v
func TestImmutability_MethodNotAllowed(t *testing.T) {
	env := newTestEnv(t)
	studentID, _ := seedStudentWithPet(t, env.store.DB(), 1, 0)
	token := env.token(studentID)
	reqBody := pointsPostReq{Reason: "作业优秀", Value: 1, RequestID: "t9-1"}

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if status, _ := env.do(t, method, "/api/points", token, reqBody); status != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/points 状态码 = %d, 期望 405", method, status)
		}
	}
	if status, _ := env.do(t, http.MethodDelete, "/api/pet/me/log", token, nil); status != http.StatusMethodNotAllowed {
		t.Error("DELETE /api/pet/me/log 状态码非 405, 期望 405（流水不可删）")
	}
	if status, _ := env.do(t, http.MethodPut, "/api/pet/me/log", token, nil); status != http.StatusMethodNotAllowed {
		t.Error("PUT /api/pet/me/log 状态码非 405, 期望 405")
	}
}

// T14 page 溢出回灌用例（对抗审查发现）：超大 page 的 (page-1)*pageSize 会整型溢出，
// SQLite 把负 OFFSET 当 0，曾导致回退返回第一页数据；修复后须拦截或返回空列表。
// 命令: go test ./server/ -run TestLog_PageOverflow_NotReturnFirstPage -v
func TestLog_PageOverflow_NotReturnFirstPage(t *testing.T) {
	env := newTestEnv(t)
	db := env.store.DB()
	studentID, petID := seedStudentWithPet(t, db, 1, 0)
	seedLogs(t, db, petID, 5)
	token := env.token(studentID)

	status, body := env.do(t, http.MethodGet, "/api/pet/me/log?page=9223372036854775807", token, nil)
	if status != http.StatusOK && status != http.StatusBadRequest {
		t.Fatalf("超大 page 状态码 = %d, 期望 200 或 400; body=%s", status, body)
	}
	if status == http.StatusOK {
		if lr := decodeLogList(t, body); len(lr.Items) != 0 {
			t.Errorf("超大 page 返回 %d 条数据, 期望空列表（不得回退第一页）; body=%s", len(lr.Items), body)
		}
	}
}
