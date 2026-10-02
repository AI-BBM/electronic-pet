package server_test

// M2 等级阈值单元用例（T11/T12）：LoadLevels / LevelFor / NextLevelPoints / DefaultLevels。
// 命令: go test ./server/ -run 'TestDefaultLevels|TestLevelFor|TestNextLevelPoints|TestLoadLevels' -v

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AI-BBM/electronic-pet/server"
)

// T12 DefaultLevels：默认阈值 Lv2=20、Lv3=60。
func TestDefaultLevels(t *testing.T) {
	if got := server.DefaultLevels(); got.Lv2 != 20 || got.Lv3 != 60 {
		t.Errorf("DefaultLevels() = %+v, 期望 {Lv2:20 Lv3:60}", got)
	}
}

// T12 LevelFor：默认与自定义阈值下的等级边界。
func TestLevelFor(t *testing.T) {
	def := server.DefaultLevels()
	cases := []struct {
		points int
		lc     server.LevelConfig
		want   int
	}{
		{0, def, 1}, {19, def, 1}, {20, def, 2}, {59, def, 2}, {60, def, 3}, {1000, def, 3},
		{4, server.LevelConfig{Lv2: 5, Lv3: 10}, 1},
		{5, server.LevelConfig{Lv2: 5, Lv3: 10}, 2},
		{9, server.LevelConfig{Lv2: 5, Lv3: 10}, 2},
		{10, server.LevelConfig{Lv2: 5, Lv3: 10}, 3},
	}
	for _, tc := range cases {
		if got := server.LevelFor(tc.points, tc.lc); got != tc.want {
			t.Errorf("LevelFor(%d, %+v) = %d, 期望 %d", tc.points, tc.lc, got, tc.want)
		}
	}
}

// T12 NextLevelPoints：1→Lv2 阈值、2→Lv3 阈值、≥3→nil（自定义阈值同理）。
func TestNextLevelPoints(t *testing.T) {
	def := server.DefaultLevels()
	custom := server.LevelConfig{Lv2: 5, Lv3: 10}
	if got := server.NextLevelPoints(1, def); got == nil || *got != 20 {
		t.Errorf("NextLevelPoints(1, def) = %v, 期望 20", got)
	}
	if got := server.NextLevelPoints(2, def); got == nil || *got != 60 {
		t.Errorf("NextLevelPoints(2, def) = %v, 期望 60", got)
	}
	if got := server.NextLevelPoints(3, def); got != nil {
		t.Errorf("NextLevelPoints(3, def) = %v, 期望 nil", *got)
	}
	if got := server.NextLevelPoints(4, def); got != nil {
		t.Errorf("NextLevelPoints(4, def) = %v, 期望 nil（超界等级不崩溃）", *got)
	}
	if got := server.NextLevelPoints(1, custom); got == nil || *got != 5 {
		t.Errorf("NextLevelPoints(1, custom) = %v, 期望 5", got)
	}
	if got := server.NextLevelPoints(2, custom); got == nil || *got != 10 {
		t.Errorf("NextLevelPoints(2, custom) = %v, 期望 10", got)
	}
	if got := server.NextLevelPoints(3, custom); got != nil {
		t.Errorf("NextLevelPoints(3, custom) = %v, 期望 nil", *got)
	}
}

// T11 LoadLevels：文件不存在回退默认；合法文件生效。
func TestLoadLevels_MissingFileFallsBackToDefault(t *testing.T) {
	lc, err := server.LoadLevels(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("文件不存在时应回退默认而非报错: %v", err)
	}
	if lc.Lv2 != 20 || lc.Lv3 != 60 {
		t.Errorf("回退阈值 = %+v, 期望 {20 60}", lc)
	}
}

func TestLoadLevels_ValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "levels.json")
	if err := os.WriteFile(path, []byte(`{"lv2":5,"lv3":10}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lc, err := server.LoadLevels(path)
	if err != nil {
		t.Fatalf("LoadLevels: %v", err)
	}
	if lc.Lv2 != 5 || lc.Lv3 != 10 {
		t.Errorf("LoadLevels = %+v, 期望 {5 10}", lc)
	}
}

// T11 LoadLevels 非法配置：坏 JSON / lv2<=0 / lv3<=lv2 一律报错。
func TestLoadLevels_InvalidConfigs(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"坏 JSON", `{lv2`},
		{"lv2=0", `{"lv2":0,"lv3":10}`},
		{"lv2 为负", `{"lv2":-3,"lv3":10}`},
		{"lv3==lv2", `{"lv2":5,"lv3":5}`},
		{"lv3<lv2", `{"lv2":10,"lv3":5}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "levels.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := server.LoadLevels(path); err == nil {
				t.Errorf("LoadLevels(%s) 期望报错, 实际 nil", tc.content)
			}
		})
	}
}
