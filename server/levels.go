package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// LevelConfig 是等级阈值（累计积分口径），服务端启动时可配置（默认 20/60）。
type LevelConfig struct {
	Lv2 int `json:"lv2"`
	Lv3 int `json:"lv3"`
}

// DefaultLevels 返回 MVP 默认阈值：Lv2=20、Lv3=60。
func DefaultLevels() LevelConfig { return LevelConfig{Lv2: 20, Lv3: 60} }

// LoadLevels 从 JSON 文件读取阈值（形如 {"lv2":20,"lv3":60}）；
// 文件不存在时返回默认值，JSON 非法或阈值关系非法（lv2<=0、lv3<=lv2）时报错。
func LoadLevels(path string) (LevelConfig, error) {
	lc := DefaultLevels()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return lc, nil
		}
		return lc, err
	}
	if err := json.Unmarshal(data, &lc); err != nil {
		return DefaultLevels(), fmt.Errorf("解析等级阈值文件 %s: %w", path, err)
	}
	if err := lc.validate(); err != nil {
		return DefaultLevels(), err
	}
	return lc, nil
}

func (c LevelConfig) validate() error {
	if c.Lv2 <= 0 {
		return fmt.Errorf("等级阈值非法: lv2=%d 必须 > 0", c.Lv2)
	}
	if c.Lv3 <= c.Lv2 {
		return fmt.Errorf("等级阈值非法: lv3=%d 必须大于 lv2=%d", c.Lv3, c.Lv2)
	}
	return nil
}

// LevelFor 按累计积分计算等级（1/2/3），满级封顶。
func LevelFor(points int, lc LevelConfig) int {
	switch {
	case points >= lc.Lv3:
		return 3
	case points >= lc.Lv2:
		return 2
	default:
		return 1
	}
}

// NextLevelPoints 返回下一等级所需累计积分；满级（>=3）返回 nil。
func NextLevelPoints(level int, lc LevelConfig) *int {
	switch level {
	case 1:
		return &lc.Lv2
	case 2:
		return &lc.Lv3
	default:
		return nil
	}
}

// LevelsFileEnv 指定等级阈值 JSON 文件的环境变量；未设置时用默认阈值 20/60。
const LevelsFileEnv = "PET_LEVELS_FILE"
