package server_test

// Issue #58 P1 缺图占位改造测试先行用例 T1–T4。
//
// 背景：#55 交付的皮肤图三级兜底（皮肤图 → s.imageUrl → s.silhouette）在阶段
// 2/3 素材缺失时把 6 款皮肤缩略图全部兜成同一张默认立绘，黄总验收看到的
// 「皮肤都是重复的」即此掩盖效应。#58 定稿：皮肤打卡照 404 一律显示
// 「素材生成中」占位，不再回退默认立绘/剪影掩盖缺图；canonical 立绘自身的
// 立绘→剪影兜底与皮肤无关，保持不动（T3 回归锚点）。
//
// 口径：
//   - T1 缩略图：皮肤卡片 <img> onerror 走占位 helper（skinImgFail→div.skin-pending
//     「素材生成中」），img 标签段内不得再出现 silhouette 兜底；
//   - T2 主图：applyDetailImg 双轨——isSkin 皮肤打卡照 404 → SKIN_PENDING 占位；
//     默认无皮肤态保留 立绘→剪影 链（使用中主图换打卡照契约 T4@issue55 演进）；
//   - T3 回归锚点：canonical 立绘（花名册卡片/表格/详情默认态）的
//     立绘→剪影 兜底不得被误删；
//   - T4 样式：占位样式 .skin-pending 已入 teacher.html 内嵌样式块。
//
// 红态口径：T1/T2/T4 断言前端尚未实现的新内容（失败即红）；T3 守护性子断言
// 红态本就通过，只在实现阶段误删 canonical 兜底时失败。后端无改动。

import (
	"strings"
	"testing"
)

// m58FnRegion 同 issue55 的 m55FnRegion：取 "function xxx" 到下一个 6 空格缩进
// 顶层 function 声明（或文末）的源码区域。
func m58FnRegion(body string, start int) string {
	next := strings.Index(body[start+1:], "\n      function ")
	if next < 0 {
		return body[start:]
	}
	return body[start : start+1+next]
}

// ---------- T1 皮肤缩略图缺图占位 ----------

// T1 皮肤卡片缩略图 404 显示「素材生成中」占位：
//
//	a) 占位 helper 存在（function skinImgFail(），其函数体创建 skin-pending
//	   占位元素且不得再回退 s.imageUrl / s.silhouette（占位即终态）；
//	b) 皮肤卡片 <img> 模板（escapeHtml(k.imageUrl) 附近）onerror 调用占位
//	   helper，且该 img 标签段内不再出现旧兜底链（silhouette）。
//
// 命令: go test ./server/ -run TestIssue58_T1_SkinThumbPendingPlaceholder -v
func TestIssue58_T1_SkinThumbPendingPlaceholder(t *testing.T) {
	body := m55Page(t, newHandler(t))

	if !m5Contains(body, "素材生成中") {
		t.Fatalf("/teacher.html 缺少「素材生成中」占位文案（#58：缺图占位取代误导性兜底）")
	}
	start := strings.Index(body, "function skinImgFail(")
	if start < 0 {
		t.Fatalf("/teacher.html 缺少占位 helper function skinImgFail(（皮肤图 404 须走「素材生成中」占位，不得回退立绘/剪影）")
	}
	region := m58FnRegion(body, start)
	if !m5Contains(region, "skin-pending") {
		t.Errorf("skinImgFail 未创建 .skin-pending 占位元素（占位样式类缺失）")
	}
	if m5Contains(region, "s.imageUrl") || m5Contains(region, "silhouette") {
		t.Errorf("skinImgFail 函数体仍在回退 s.imageUrl/s.silhouette：占位必须是终态，不得再兜底默认立绘掩盖缺图（#58 定稿）")
	}

	// 皮肤卡片 img 模板段：onerror 走 helper，旧兜底链已移除。
	i := strings.Index(body, "escapeHtml(k.imageUrl)")
	if i < 0 {
		t.Fatalf("/teacher.html 缺少皮肤卡片图模板（escapeHtml(k.imageUrl)）：缩略图契约无从验证")
	}
	lo := i - 120
	if lo < 0 {
		lo = 0
	}
	seg := body[lo:min(i+420, len(body))]
	if !m5Contains(seg, `onerror="skinImgFail(this)"`) {
		t.Errorf("皮肤卡片 <img> onerror 未调用 skinImgFail：404 时缩略图须显示「素材生成中」占位（当前段=%q）", seg)
	}
	if m5Contains(seg, "silhouette") {
		t.Errorf("皮肤卡片 <img> 段仍含 silhouette 兜底：皮肤图 404 不得回退剪影（掩盖缺图正是黄总看到的「皮肤都重复」根因）")
	}
}

// ---------- T2 使用中主图缺图占位（applyDetailImg 双轨） ----------

// T2 详情主图缺图双轨：
//
//	a) applyDetailImg 定义含皮肤占位分支（SKIN_PENDING data-URI，文案「素材生成中」）；
//	b) 使用中皮肤主图调用点（applyDetailImg(s, data.skins[i].imageUrl）带 isSkin
//	   标记（第三参 true），皮肤打卡照 404 → 占位而非回退立绘；
//	c) 默认无皮肤态调用点 applyDetailImg(s, s.imageUrl) 不带标记——canonical
//	   立绘仍走 立绘→剪影 既有链（T3 回归锚点在函数体层面钉住）。
//
// 命令: go test ./server/ -run TestIssue58_T2_ActiveSkinMainImagePending -v
func TestIssue58_T2_ActiveSkinMainImagePending(t *testing.T) {
	body := m55Page(t, newHandler(t))

	if !m5Contains(body, "SKIN_PENDING") {
		t.Fatalf("/teacher.html 缺少 SKIN_PENDING 占位图常量（皮肤打卡照 404 的主图占位未实现）")
	}
	if !m5Contains(body, "data:image/svg+xml") {
		t.Errorf("SKIN_PENDING 未用 data-URI 内联占位（皮肤图 404 时主图须显示「素材生成中」，不得再发起必然失败的网络请求）")
	}
	start := strings.Index(body, "function applyDetailImg(")
	if start < 0 {
		t.Fatalf("/teacher.html 缺少 applyDetailImg 函数（既有结构被破坏？）")
	}
	region := m58FnRegion(body, start)
	if !m5Contains(region, "SKIN_PENDING") {
		t.Errorf("applyDetailImg 函数体未引用 SKIN_PENDING：使用中皮肤打卡照 404 须显示「素材生成中」占位")
	}
	if !m5Contains(region, "s.silhouette") {
		t.Errorf("applyDetailImg 函数体丢失 s.silhouette 链：默认无皮肤态的 canonical 立绘兜底不得回退（#58 只改皮肤图轨）")
	}

	// 使用中皮肤调用点带 isSkin 标记。
	i := strings.Index(body, "applyDetailImg(s, data.skins[i].imageUrl")
	if i < 0 {
		t.Fatalf("/teacher.html 缺少使用中皮肤主图调用点（applyDetailImg(s, data.skins[i].imageUrl...）")
	}
	w := body[i:min(i+80, len(body))]
	if !m5Contains(w, ", true)") {
		t.Errorf("使用中皮肤主图调用未带 isSkin 标记（第三参 true）：皮肤打卡照 404 会误走 canonical 兜底链（当前=%q）", w)
	}
	// 默认态调用点不带标记。
	j := strings.Index(body, "applyDetailImg(s, s.imageUrl)")
	if j < 0 {
		t.Fatalf("/teacher.html 缺少默认无皮肤态主图调用点 applyDetailImg(s, s.imageUrl)")
	}
	if m5Contains(body[j:min(j+40, len(body))], ", true)") {
		t.Errorf("默认态主图调用被误标 isSkin=true：canonical 立绘缺图会显示「素材生成中」而非剪影兜底（超范围改动）")
	}
}

// ---------- T3 canonical 立绘兜底回归锚点 ----------

// T3 与皮肤无关的 canonical 立绘兜底不得被本次改造误删：花名册卡片墙
// （loadRoster）、petImgTag 表格行、openDetail 默认态三处的
// 立绘→剪影 onerror 链必须仍在。
// 命令: go test ./server/ -run TestIssue58_T3_CanonicalFallbackUntouched -v
func TestIssue58_T3_CanonicalFallbackUntouched(t *testing.T) {
	body := m55Page(t, newHandler(t))

	cases := []struct {
		fn    string
		where string
	}{
		{"function petImgTag(", "表格行 canonical 立绘"},
		{"function loadRoster(", "卡片墙 canonical 立绘"},
		{"function openDetail(", "详情默认态 canonical 立绘"},
	}
	for _, c := range cases {
		start := strings.Index(body, c.fn)
		if start < 0 {
			t.Fatalf("/teacher.html 缺少 %s（既有结构被破坏？）", c.fn)
		}
		if !m5Contains(m58FnRegion(body, start), "silhouette") {
			t.Errorf("%s 的 立绘→剪影 兜底链丢失：#58 只移除皮肤图轨的误导性兜底，canonical 立绘兜底不得动", c.where)
		}
	}
}

// ---------- T4 占位样式 ----------

// T4 .skin-pending 占位样式已入 teacher.html 内嵌样式块（与 .skin-card 同域，
// 含居中排版证据）。
// 命令: go test ./server/ -run TestIssue58_T4_PendingStyleRule -v
func TestIssue58_T4_PendingStyleRule(t *testing.T) {
	body := m55Page(t, newHandler(t))

	if !m5Contains(body, ".skin-pending") {
		t.Fatalf("teacher.html 内嵌样式缺少 .skin-pending 规则（占位块无样式会裸成空白）")
	}
	if w, ok := m55Around(body, ".skin-pending", 0, 260); ok {
		if !m5Contains(w, "flex") {
			t.Errorf(".skin-pending 规则缺少居中排版（flex 居中），「素材生成中」文案可能偏置/溢出")
		}
	}
}

// min 复用 issue35_test.go 的包内定义（go1.26 内建 min 语义一致）。
