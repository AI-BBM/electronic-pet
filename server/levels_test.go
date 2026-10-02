package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// T12 DefaultLevels：默认阈值 Lv2=20、Lv3=60。
// 命令: go test ./server/ -run TestDefaultLevels -v
func TestDefaultLevels(t *testing.T) {
	lc := DefaultLevels()
	if lc.Lv2 != 20 || lc.Lv3 != 60 {
		t.Errorf("DefaultLevels() = %+v, 期望 {Lv2:20 Lv3:60}", lc)
	}
}

// T12 LevelFor 边界：默认与自定义阈值下的关键分值点。
// 命令: go test ./server/ -run TestLevelFor -v
func TestLevelFor(t *testing.T) {
	def := DefaultLevels()
	custom := LevelConfig{Lv2: 5, Lv3: 10}
	cases := []struct {
		name   string
		points int
		lc     LevelConfig
		want   int
	}{
		{"默认 0 分 → Lv1", 0, def, 1},
		{"默认 19 分 → Lv1", 19, def, 1},
		{"默认 20 分 → Lv2", 20, def, 2},
		{"默认 59 分 → Lv2", 59, def, 2},
		{"默认 60 分 → Lv3", 60, def, 3},
		{"默认 1000 分 → Lv3", 1000, def, 3},
		{"自定义 {5,10} 4 分 → Lv1", 4, custom, 1},
		{"自定义 {5,10} 5 分 → Lv2", 5, custom, 2},
		{"自定义 {5,10} 9 分 → Lv2", 9, custom, 2},
		{"自定义 {5,10} 10 分 → Lv3", 10, custom, 3},
	}
	for _, tc := range cases {
		if got := LevelFor(tc.points, tc.lc); got != tc.want {
			t.Errorf("%s: LevelFor(%d, %+v) = %d, 期望 %d", tc.name, tc.points, tc.lc, got, tc.want)
		}
	}
}

// T12 NextLevelPoints：Lv1/Lv2 返回下一级阈值指针，Lv3 及以上返回 nil。
// 命令: go test ./server/ -run TestNextLevelPoints -v
func TestNextLevelPoints(t *testing.T) {
	def := DefaultLevels()
	custom := LevelConfig{Lv2: 5, Lv3: 10}

	if p := NextLevelPoints(1, def); p == nil {
		t.Error("默认阈值 Lv1 的 nextLevelPoints = nil, 期望 20")
	} else if *p != 20 {
		t.Errorf("默认阈值 Lv1 的 nextLevelPoints = %d, 期望 20", *p)
	}
	if p := NextLevelPoints(2, def); p == nil {
		t.Error("默认阈值 Lv2 的 nextLevelPoints = nil, 期望 60")
	} else if *p != 60 {
		t.Errorf("默认阈值 Lv2 的 nextLevelPoints = %d, 期望 60", *p)
	}
	if p := NextLevelPoints(3, def); p != nil {
		t.Errorf("默认阈值 Lv3 的 nextLevelPoints = %d, 期望 nil", *p)
	}
	if p := NextLevelPoints(4, def); p != nil {
		t.Errorf("超过满级(level=4)的 nextLevelPoints = %d, 期望 nil", *p)
	}
	if p := NextLevelPoints(1, custom); p == nil {
		t.Error("自定义阈值 Lv1 的 nextLevelPoints = nil, 期望 5")
	} else if *p != 5 {
		t.Errorf("自定义阈值 Lv1 的 nextLevelPoints = %d, 期望 5", *p)
	}
	if p := NextLevelPoints(2, custom); p == nil {
		t.Error("自定义阈值 Lv2 的 nextLevelPoints = nil, 期望 10")
	} else if *p != 10 {
		t.Errorf("自定义阈值 Lv2 的 nextLevelPoints = %d, 期望 10", *p)
	}
	if p := NextLevelPoints(3, custom); p != nil {
		t.Errorf("自定义阈值 Lv3 的 nextLevelPoints = %d, 期望 nil", *p)
	}
}

// T11 LoadLevels：文件不存在 → 返回默认值且不报错。
// 命令: go test ./server/ -run TestLoadLevels_MissingFileFallsBackToDefault -v
func TestLoadLevels_MissingFileFallsBackToDefault(t *testing.T) {
	lc, err := LoadLevels(filepath.Join(t.TempDir(), "no-such-levels.json"))
	if err != nil {
		t.Fatalf("文件不存在时 LoadLevels 不应报错: %v", err)
	}
	if lc.Lv2 != 20 || lc.Lv3 != 60 {
		t.Errorf("文件不存在时 LoadLevels = %+v, 期望默认 {Lv2:20 Lv3:60}", lc)
	}
}

// T11 LoadLevels：合法 JSON 配置 {"lv2":5,"lv3":10} 生效。
// 命令: go test ./server/ -run TestLoadLevels_ValidFile -v
func TestLoadLevels_ValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "levels.json")
	if err := os.WriteFile(path, []byte(`{"lv2":5,"lv3":10}`), 0o644); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	lc, err := LoadLevels(path)
	if err != nil {
		t.Fatalf("合法配置 LoadLevels 报错: %v", err)
	}
	if lc.Lv2 != 5 || lc.Lv3 != 10 {
		t.Errorf("LoadLevels = %+v, 期望 {Lv2:5 Lv3:10}", lc)
	}
}

// T11 LoadLevels：坏 JSON、lv2<=0、lv3<=lv2 均必须报错。
// 命令: go test ./server/ -run TestLoadLevels_InvalidConfigs -v
func TestLoadLevels_InvalidConfigs(t *testing.T) {
	cases := []struct{ name, content string }{
		{"坏 JSON", `{"lv2":5,`},
		{"lv2 为 0", `{"lv2":0,"lv3":10}`},
		{"lv2 为负数", `{"lv2":-3,"lv3":10}`},
		{"lv3 等于 lv2", `{"lv2":10,"lv3":10}`},
		{"lv3 小于 lv2", `{"lv2":20,"lv3":5}`},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "levels.json")
		if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
			t.Fatalf("%s: 写入临时配置失败: %v", tc.name, err)
		}
		if _, err := LoadLevels(path); err == nil {
			t.Errorf("%s: LoadLevels 内容 %s 应当报错, 却返回 nil", tc.name, tc.content)
		}
	}
}

// T11 阈值可配置端到端：{"lv2":5,"lv3":10} 下 4+1 触发 Lv2，再 +5 达 Lv3 满级。
// 命令: go test ./server/ -run TestAddPoints_CustomLevels_EndToEnd -v
func TestAddPoints_CustomLevels_EndToEnd(t *testing.T) {
	env := newTestEnvWithLevels(t, LevelConfig{Lv2: 5, Lv3: 10})
	studentID, _ := seedStudentWithPet(t, env.store.DB(), 1, 4)

	status, body := env.do(t, http.MethodPost, "/api/points", env.token(studentID),
		pointsPostReq{Reason: "课堂表现", Value: 1, RequestID: "t11-1"})
	if status != http.StatusOK {
		t.Fatalf("4+1 加分状态码 = %d, 期望 200; body=%s", status, body)
	}
	resp := decodePointsResp(t, body)
	if !resp.LevelUp || resp.Level != 2 || resp.Pet.Points != 5 {
		t.Errorf("自定义阈值 4+1 后 = (levelUp=%v level=%d points=%d), 期望 (true, 2, 5)",
			resp.LevelUp, resp.Level, resp.Pet.Points)
	}
	if raw, ok := rawPetField(t, body, "nextLevelPoints"); !ok || raw != "10" {
		t.Errorf("自定义阈值 Lv2 的 nextLevelPoints 原始 JSON = %q(ok=%v), 期望 \"10\"", raw, ok)
	}

	status, body = env.do(t, http.MethodPost, "/api/points", env.token(studentID),
		pointsPostReq{Reason: "劳动卫生", Value: 5, RequestID: "t11-2"})
	if status != http.StatusOK {
		t.Fatalf("5+5 加分状态码 = %d, 期望 200; body=%s", status, body)
	}
	resp = decodePointsResp(t, body)
	if !resp.LevelUp || resp.Level != 3 || resp.Pet.Points != 10 {
		t.Errorf("自定义阈值 5+5 后 = (levelUp=%v level=%d points=%d), 期望 (true, 3, 10)",
			resp.LevelUp, resp.Level, resp.Pet.Points)
	}
	if raw, ok := rawPetField(t, body, "nextLevelPoints"); !ok || raw != "null" {
		t.Errorf("自定义阈值满级 nextLevelPoints 原始 JSON = %q(ok=%v), 期望 \"null\"", raw, ok)
	}
}
