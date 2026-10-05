package server_test

// Issue #37 M9 帧动画试点测试（前端 anim 优先回退 + 管线工具存在性）。
// 素材生产管线（tools/assets/make_idle_anim.py）为 Python 工具，本文件守护其
// 前端接入契约：anim.webp 优先、png 二级回退、silhouette 三级兜底。

import (
	"testing"
)

func TestM9_FrontendAnimFallbackAnchors(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`function animOf(url)`,            // URL 变换助手
		`-anim.webp`,                      // 动画 URL 约定
		`.png', '-anim.webp')`,            // 精确替换逻辑
		`this.dataset.f=1`,                // 首跳回退标记（anim→png）
		`escapeHtml(animOf(s.imageUrl))`,  // 卡片/表格走 anim
		`animOf(s.imageUrl)`,              // 详情大图走 anim
		`this.onerror = function () { this.onerror = null; this.src = s.silhouette; }; this.src = s.imageUrl;`, // 详情三级兜底（anim→png→silhouette）
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少 M9 anim 锚点 %q", anchor)
		}
	}
}

func TestM9_AnimToolPlaceholder(t *testing.T) {
	// 管线工具 make_idle_anim.py 为 Python 交付物（真实验证在 PR 流程 shell 步骤）；
	// Go 侧守护 server 页面基线不回归即可。
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")
	if !m5Contains(body, `id="card-wall"`) {
		t.Fatalf("teacher.html 缺少卡片墙（M8 基线破坏）")
	}
}
