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

// 素材 URL：#10 已上传的公网直链 manifest（design/assets_output/manifest.json，version=1）
// 现值，结构对齐 OSS 目录契约 pets/{species}/{1,2,3,silhouette}.png。
// #14 签名 URL 版 manifest 落地后，由服务端动态下发统一替换这批常量。
const ossBaseURL = "https://laoli-storage.oss-cn-beijing.aliyuncs.com/pets"

func ossStageURL(id string, stage int) string {
	return fmt.Sprintf("%s/%s/%d.png", ossBaseURL, id, stage)
}

func ossSilhouetteURL(id string) string {
	return fmt.Sprintf("%s/%s/silhouette.png", ossBaseURL, id)
}

// stages3 生成某物种的三阶段直链（manifest 结构占位）。
func stages3(id string) []string {
	return []string{ossStageURL(id, 1), ossStageURL(id, 2), ossStageURL(id, 3)}
}

var speciesList = []speciesInfo{
	{"cat", "电力猫", rarityCommon, stages3("cat"), ossSilhouetteURL("cat")},
	{"dog", "像素狗", rarityCommon, stages3("dog"), ossSilhouetteURL("dog")},
	{"bunny", "云绒兔", rarityCommon, stages3("bunny"), ossSilhouetteURL("bunny")},
	{"hamster", "芯片仓鼠", rarityCommon, stages3("hamster"), ossSilhouetteURL("hamster")},
	{"chick", "蛋壳鸡", rarityCommon, stages3("chick"), ossSilhouetteURL("chick")},
	{"penguin", "冰川企鹅", rarityCommon, stages3("penguin"), ossSilhouetteURL("penguin")},
	{"koala", "电池考拉", rarityCommon, stages3("koala"), ossSilhouetteURL("koala")},
	{"axolotl", "六角恐龙", rarityCommon, stages3("axolotl"), ossSilhouetteURL("axolotl")},
	{"fox", "星辰狐", rarityRare, stages3("fox"), ossSilhouetteURL("fox")},
	{"panda", "太极熊猫", rarityRare, stages3("panda"), ossSilhouetteURL("panda")},
	{"dino", "机械恐龙", rarityRare, stages3("dino"), ossSilhouetteURL("dino")},
	{"dragon", "神威小龙", rarityEpic, stages3("dragon"), ossSilhouetteURL("dragon")},
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
