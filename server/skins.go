package server

// M12（#51）皮肤目录 v3（黄总 22:40 澄清定稿）：皮肤 = 宠物置身场景的打卡照
// （吃饭/睡觉/游乐园/游泳/海边/旅游 6 款），使用中 = 详情页主图直接换打卡照。
// 首批资产 36 张 = 2 物种 × 3 阶段 × 6 场景（glm-53 生产）；各款 5 积分；
// 购买永久解锁（pet_skins 随宠物软删除保留，硬清理随宠物清除）。

// skinScene 皮肤场景款（场景维度，资产按物种×阶段派生）。
type skinScene struct {
	ID   string `json:"skinId"`
	Name string `json:"name"`
}

var skinScenes = []skinScene{
	{ID: "meal", Name: "干饭"},
	{ID: "sleep", Name: "睡觉"},
	{ID: "park", Name: "游乐园"},
	{ID: "swim", Name: "游泳"},
	{ID: "beach", Name: "海边"},
	{ID: "travel", Name: "旅游"},
}

// SkinPrice 每款统一价（积分）。
const SkinPrice = 5

// skinByScene 按 skinId 查场景款。
func skinByScene(skinID string) (skinScene, bool) {
	for _, sc := range skinScenes {
		if sc.ID == skinID {
			return sc, true
		}
	}
	return skinScene{}, false
}

// stageForLevel 阶段映射：等级 1/2/3 即阶段，满级封顶（levels.go 同口径）。
func stageForLevel(level int) int {
	if level >= 3 {
		return 3
	}
	if level < 1 {
		return 1
	}
	return level
}

// skinImageURL 打卡照直链。资产命名约定（与 glm-53 的生产路线联调时对齐，
// 若实际路径不同只需改本函数）：
// skins/{species}_{stage}_{skin}.jpg，如 skins/bunny_2_park.jpg
func skinImageURL(speciesID string, stage int, skinID string) string {
	return "https://pet-aibbm-assets.oss-cn-hangzhou.aliyuncs.com/skins/" +
		speciesID + "_" + itoa(stage) + "_" + skinID + ".jpg"
}

// itoa 小整数转十进制串（保持本文件内聚，不为此引入 strconv）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
