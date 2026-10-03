package server

import (
	"fmt"
	"math/rand/v2"
)

// species manifest（服务端内置版）：id/name/rarity 对齐 canonical 定稿表
// tools/assets/species.meta.json；stages/silhouette 为 OSS 直链（见下方 ossBaseURL）。
// 正式 manifest（version/eggs/species[id,name,rarity,stages,silhouette]）由素材线
// Issue #4 管线产出，#14 签名 URL 版落地后由服务端动态下发替换。
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

// 素材 URL 源（M3 过渡态）：全部为二进制内嵌本地资源。
// OSS 直链暂不可用（现桶 403，且黄总已定裁迁移至新桶 pet-aibbm-assets，前缀将变），
// 故 stages 用探索稿占位循环、silhouette 用预生成剪影内嵌（见 web/static/img/pets/）。
// #14 签名 URL 版 manifest 落地后，仅替换本节两个函数为动态下发实现（键约定 pets/{id}/{1|2|3|silhouette}.png 不变）。

// placeholderArt 是 M1 探索稿占位立绘（内嵌，恒可用）。
var placeholderArt = []string{"/img/pets/robotcat.png", "/img/pets/pixeldog.png", "/img/pets/bunny.png"}

// placeholderStages 返回第 i 个物种的三阶段占位（轮转，Lv1 图与 M1 一致）。
func placeholderStages(i int) []string {
	return []string{placeholderArt[i%3], placeholderArt[(i+1)%3], placeholderArt[(i+2)%3]}
}

func silhouetteURL(id string) string {
	return "/img/pets/" + id + "/silhouette.png"
}

var speciesList = []speciesInfo{
	{"cat", "电力猫", rarityCommon, placeholderStages(0), silhouetteURL("cat")},
	{"dog", "像素狗", rarityCommon, placeholderStages(1), silhouetteURL("dog")},
	{"bunny", "云绒兔", rarityCommon, placeholderStages(2), silhouetteURL("bunny")},
	{"hamster", "芯片仓鼠", rarityCommon, placeholderStages(3), silhouetteURL("hamster")},
	{"chick", "蛋壳鸡", rarityCommon, placeholderStages(4), silhouetteURL("chick")},
	{"penguin", "冰川企鹅", rarityCommon, placeholderStages(5), silhouetteURL("penguin")},
	{"koala", "电池考拉", rarityCommon, placeholderStages(6), silhouetteURL("koala")},
	{"axolotl", "六角恐龙", rarityCommon, placeholderStages(7), silhouetteURL("axolotl")},
	{"fox", "星辰狐", rarityRare, placeholderStages(8), silhouetteURL("fox")},
	{"panda", "太极熊猫", rarityRare, placeholderStages(9), silhouetteURL("panda")},
	{"dino", "机械恐龙", rarityRare, placeholderStages(10), silhouetteURL("dino")},
	{"dragon", "神威小龙", rarityEpic, placeholderStages(11), silhouetteURL("dragon")},
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

// 升级阈值由 srv.levels 持有（默认 Lv2=20、Lv3=60，启动时经 PET_LEVELS_FILE 可配置，见 levels.go）。

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

func petJSON(petID int64, name string, level, points int, s speciesInfo, lc LevelConfig) map[string]any {
	return map[string]any{
		"id":              petID,
		"name":            name,
		"species":         speciesJSON(s, level),
		"level":           level,
		"points":          points,
		"nextLevelPoints": NextLevelPoints(level, lc),
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
