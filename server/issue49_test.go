package server_test

// Issue #49 M11 F1：学生详情页场景背景按时段切换（黄总 2026-10-06 需求）。
// 守护前端契约：5 时段映射（清晨/白天/傍晚/夜晚/深夜）、默认兜底白天、
// 加载失败纯色兜底、无需刷新定时切换。

import (
	"testing"
)

func TestM11_FrontendSceneSwitchAnchors(t *testing.T) {
	h := newHandler(t)
	body := m5Get(t, h, "/teacher.html")

	for _, anchor := range []string{
		`function sceneUrl(now)`, // 时段→场景 URL 映射助手
		`scenes/`,                // OSS 场景图路径约定
		`id="detail-scene"`,      // 详情页场景底层容器
		`detail-scene`,           // 场景层样式挂接
		`daytime.jpg`,            // 默认兜底白天
		`onerror`,                // 加载失败兜底（纯色）
		`setInterval`,            // 定时切换无需刷新
	} {
		if !m5Contains(body, anchor) {
			t.Errorf("teacher.html 缺少 M11 场景切换锚点 %q", anchor)
		}
	}

	// 反向：M11 不得引入动画引用回潮
	for _, banned := range []string{`animOf`, `-anim.webp`} {
		if m5Contains(body, banned) {
			t.Errorf("teacher.html 出现动画死引用 %q（#46 H1 回潮）", banned)
		}
	}
}
