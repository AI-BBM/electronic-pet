package server

import (
	"fmt"
	"math/rand/v2"
)

// 占位 manifest：正式 manifest（version/eggs/species[id,name,rarity,stages,silhouette]）
// 由素材线 Issue #4 交付后替换，species.id 为稳定 slug，替换时仅换 stages 的 URL。
// M1 仅幼年期（stages[0]），silhouette 留空。
// id/name/rarity 已对齐 canonical 物种定稿表 tools/assets/species.meta.json（1d00e4c）；
// 立绘仍为 design/characters/ 探索稿降采样占位（三张循环）。
type speciesInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Rarity     string   `json:"rarity"`
	Stages     []string `json:"stages"`
	Silhouette string   `json:"silhouette"`
}

const (
	rarityCommon = "common"
	rarityRare   = "rare"
	rarityEpic   = "epic"
)

var speciesList = []speciesInfo{
	{"cat", "电力猫", rarityCommon, []string{"/img/pets/robotcat.png"}, ""},
	{"dog", "像素狗", rarityCommon, []string{"/img/pets/pixeldog.png"}, ""},
	{"bunny", "云绒兔", rarityCommon, []string{"/img/pets/bunny.png"}, ""},
	{"hamster", "芯片仓鼠", rarityCommon, []string{"/img/pets/robotcat.png"}, ""},
	{"chick", "蛋壳鸡", rarityCommon, []string{"/img/pets/pixeldog.png"}, ""},
	{"penguin", "冰川企鹅", rarityCommon, []string{"/img/pets/bunny.png"}, ""},
	{"koala", "电池考拉", rarityCommon, []string{"/img/pets/robotcat.png"}, ""},
	{"axolotl", "六角恐龙", rarityCommon, []string{"/img/pets/pixeldog.png"}, ""},
	{"fox", "星辰狐", rarityRare, []string{"/img/pets/bunny.png"}, ""},
	{"panda", "太极熊猫", rarityRare, []string{"/img/pets/robotcat.png"}, ""},
	{"dino", "机械恐龙", rarityRare, []string{"/img/pets/pixeldog.png"}, ""},
	{"dragon", "神威小龙", rarityEpic, []string{"/img/pets/robotcat.png"}, ""},
}

// 稀有度权重：普通 70% / 稀有 25% / 史诗 5%（PRD M1）。
var rarityWeights = map[string]int{
	rarityCommon: 70,
	rarityRare:   25,
	rarityEpic:   5,
}

var speciesByID = func() map[string]speciesInfo {
	m := make(map[string]speciesInfo, len(speciesList))
	for _, s := range speciesList {
		m[s.ID] = s
	}
	return m
}()

// pickSpecies 按稀有度加权后在该稀有度内均匀随机。
func pickSpecies() speciesInfo {
	total := 0
	for _, w := range rarityWeights {
		total += w
	}
	r := rand.IntN(total)
	chosen := rarityCommon
	for _, rarity := range []string{rarityEpic, rarityRare, rarityCommon} {
		r -= rarityWeights[rarity]
		if r < 0 {
			chosen = rarity
			break
		}
	}
	var pool []speciesInfo
	for _, s := range speciesList {
		if s.Rarity == chosen {
			pool = append(pool, s)
		}
	}
	return pool[rand.IntN(len(pool))]
}

type eggInfo struct {
	ID    string `json:"id"`
	Color string `json:"color"`
}

// 蛋仅视觉差异（PRD：颜色不影响孵化结果）。
var eggList = []eggInfo{
	{"egg-1", "#FFD166"},
	{"egg-2", "#06D6A0"},
	{"egg-3", "#4CC9F0"},
	{"egg-4", "#F72585"},
	{"egg-5", "#9B5DE5"},
	{"egg-6", "#FB8500"},
}

func validEgg(id string) bool {
	for _, e := range eggList {
		if e.ID == id {
			return true
		}
	}
	return false
}

// 升级阈值（累计积分）：Lv2=20、Lv3=60。M1 写死；M2 将改为启动可配置。
var levelThresholds = []int{20, 60}

// nextLevelPoints 返回该等级的下一级累计积分目标，满级返回 nil。
func nextLevelPoints(level int) *int {
	if level-1 < 0 || level-1 >= len(levelThresholds) {
		return nil
	}
	v := levelThresholds[level-1]
	return &v
}

func speciesImageURL(s speciesInfo, level int) string {
	idx := level - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s.Stages) {
		idx = len(s.Stages) - 1
	}
	if idx < 0 || len(s.Stages) == 0 {
		return ""
	}
	return s.Stages[idx]
}

func speciesJSON(s speciesInfo, level int) map[string]any {
	return map[string]any{
		"id":         s.ID,
		"name":       s.Name,
		"rarity":     s.Rarity,
		"imageUrl":   speciesImageURL(s, level),
		"silhouette": nilIfEmpty(s.Silhouette),
	}
}

func petJSON(petID int64, name string, level, points int, s speciesInfo) map[string]any {
	return map[string]any{
		"id":              petID,
		"name":            name,
		"species":         speciesJSON(s, level),
		"level":           level,
		"points":          points,
		"nextLevelPoints": nextLevelPoints(level),
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func errJSON(format string, args ...any) map[string]any {
	return map[string]any{"error": fmt.Sprintf(format, args...)}
}
