package server_test

// Issue #37 M9 → #46 善后：黄总 2026-10-06 拍板终止动画线（"动的视频生成
// 效果不好"）。本文件改守**静态直出契约**：宠物图直接用静态 png、失败兜底
// 剪影；teacher.html 不得再引用 -anim.webp（OSS 动画对象已下线 404）。

import (
	"strings"
	"testing"
)

func TestM9_FrontendStaticDirectAnchors(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	// 反向锚点：动画死引用必须清干净
	for _, banned := range []string{
		`animOf`,
		`-anim.webp`,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("teacher.html 仍含动画死引用 %q（#46 H1：应直出静态 png）", banned)
		}
	}

	// 正向锚点：静态直出 + 剪影兜底
	for _, anchor := range []string{
		`escapeHtml(s.imageUrl)`,       // 卡片/表格直出静态
		`$('detail-pet-img').src = s.imageUrl;`, // 详情大图直出静态
		`this.dataset.f=1`,             // png 失败首跳剪影标记
		`this.onerror=null;this.src=\'`, // 兜底链闭合（HTML 内转义单引号）
		`escapeHtml(s.silhouette)`,     // 剪影兜底
		`id="card-wall"`,               // M8 卡片墙基线
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少静态直出锚点 %q", anchor)
		}
	}
}
