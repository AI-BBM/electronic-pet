package server_test

// Issue #18 测试先行：species.go 占位图切换 OSS 直链真素材的扩展契约。
// - TestOssEggsContract：GET /api/eggs 向后兼容加字段（rarity/imageUrl），
//   id/color 语义不变（M1 断言不破），rarity 分配固定 egg-1/2→common、
//   egg-3/4→rare、egg-5/6→epic，imageUrl 为桶直链且文件名与 rarity 同源。
// - TestOssAssetURLsFetchable：dex 全部素材 URL + eggs 全部 imageUrl 真实可取回
//   （200 + image/png）。绝对 URL 外网取回，需 PET_SMOKE_OSS=1 门控，
//   未设则 Skip，与 TestM3_AssetURLsFetchable 同口径；并复验锁定条目
//   stages=null 红线（未解锁不得泄露彩色立绘 URL）。
//
// 黑盒风格：只经真实 API（join/adopt）造数据，复用 M1/M3 helpers，不感知内部实现。

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// OSS 桶直链前缀（manifest.json version=1 定稿，Issue #10 迁移后的生产桶）。
const (
	ossBucketBase = "https://pet-aibbm-assets.oss-cn-hangzhou.aliyuncs.com/"
	ossEggsPrefix = ossBucketBase + "eggs/"
	ossPetsPrefix = ossBucketBase + "pets/"
)

// ossEgg 是 GET /api/eggs 单颗蛋的类型化字段集（M1 的 id/color + #18 新增 rarity/imageUrl）。
type ossEgg struct {
	ID       string `json:"id"`
	Color    string `json:"color"`
	Rarity   string `json:"rarity"`
	ImageURL string `json:"imageUrl"`
}

// ossEggsResp 是 GET /api/eggs 的响应体形状。
type ossEggsResp struct {
	Eggs []ossEgg `json:"eggs"`
}

// ossWantEggRarity 是 #18 定稿的蛋→稀有度分配表。
var ossWantEggRarity = map[string]string{
	"egg-1": "common",
	"egg-2": "common",
	"egg-3": "rare",
	"egg-4": "rare",
	"egg-5": "epic",
	"egg-6": "epic",
}

// TestOssEggsContract 校验 /api/eggs 扩展契约：恰好 6 颗、id 唯一、color 唯一非空、
// rarity ∈ {common,rare,epic} 且分配恰为 egg-1/2→common、egg-3/4→rare、egg-5/6→epic、
// imageUrl 非空且以桶 eggs/ 前缀开头、文件名与该 rarity 一致（同源自 manifest eggs 键）。
func TestOssEggsContract(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c1", "小明", "01")

	resp, body := doJSON(t, h, http.MethodGet, "/api/eggs", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/eggs 状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
	}
	// 再解码为类型化视图做精确断言（缺键语义：rarity/imageUrl 缺失时为 ""，仍判失败）。
	rawBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("重新编码 eggs 响应失败: %v", err)
	}
	var typed ossEggsResp
	if err := json.Unmarshal(rawBody, &typed); err != nil {
		t.Fatalf("GET /api/eggs 响应解码失败: %v", err)
	}
	eggs := typed.Eggs
	if len(eggs) != 6 {
		t.Fatalf("eggs 数量 = %d, 期望恰好 6（%+v）", len(eggs), eggs)
	}

	seenID := make(map[string]bool, len(eggs))
	seenColor := make(map[string]bool, len(eggs))
	for i, e := range eggs {
		ctx := "eggs[" + e.ID + "]"
		if e.ID == "" {
			t.Errorf("第 %d 颗蛋 id 为空", i)
			continue
		}
		if seenID[e.ID] {
			t.Errorf("%s: id 重复", ctx)
		}
		seenID[e.ID] = true

		if e.Color == "" {
			t.Errorf("%s: color 为空", ctx)
		} else if seenColor[e.Color] {
			t.Errorf("%s: color 重复 %q（颜色必须互异）", ctx, e.Color)
		}
		seenColor[e.Color] = true

		want, ok := ossWantEggRarity[e.ID]
		if !ok {
			t.Errorf("%s: 不在 #18 蛋→稀有度分配表中的意外 id", ctx)
			continue
		}
		if e.Rarity != want {
			t.Errorf("%s: rarity = %q, 期望 %q", ctx, e.Rarity, want)
		}
		if !isValidRarity(e.Rarity) {
			t.Errorf("%s: rarity = %q, 期望 common/rare/epic 之一", ctx, e.Rarity)
		}

		if e.ImageURL == "" {
			t.Errorf("%s: imageUrl 为空", ctx)
			continue
		}
		if !strings.HasPrefix(e.ImageURL, ossEggsPrefix) {
			t.Errorf("%s: imageUrl = %q, 期望以 %q 开头", ctx, e.ImageURL, ossEggsPrefix)
		}
		wantSuffix := "/eggs/" + e.Rarity + ".png"
		if !strings.HasSuffix(e.ImageURL, wantSuffix) {
			t.Errorf("%s: imageUrl = %q, 期望以 %q 结尾（文件名须与 rarity 同源）", ctx, e.ImageURL, wantSuffix)
		}
	}
}

// TestOssAssetURLsFetchable 校验 OSS 直链真实可取回（需 PET_SMOKE_OSS=1 门控）：
// a) adopt 后 dex 全部 12 条 silhouette 逐个 GET 断言 200 + image/png，
//    解锁条目 stages 3 个逐个 GET 断言，锁定条目 stages 键为 null（红线复验）；
// b) eggs 全部 6 个 imageUrl 逐个 GET 断言 200 + image/png；
// c) 解锁物种数 ≥1（否则断言失效 Fatal）。
func TestOssAssetURLsFetchable(t *testing.T) {
	if os.Getenv("PET_SMOKE_OSS") == "" {
		t.Skip("外网 OSS 可取回检查需 PET_SMOKE_OSS=1")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	fetch := func(url, ctx string) {
		t.Helper()
		resp, err := client.Get(url)
		if err != nil {
			t.Errorf("%s: GET %s 失败: %v", ctx, url, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: GET %s 状态码 = %d, 期望 200", ctx, url, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
			t.Errorf("%s: GET %s Content-Type = %q, 期望 image/png", ctx, url, ct)
		}
	}

	h := newHandler(t)
	token := mustJoin(t, h, "c1", "小明", "01")
	mustAdopt(t, h, token, "random") // 保证至少一个解锁物种

	// a) dex：12 条 silhouette + 解锁条目 stages 全部可取回；锁定条目 stages=null 红线复验。
	status, dex, raw := m3GetDex(t, h, token)
	if status != http.StatusOK {
		t.Fatalf("GET /api/dex 状态码 = %d, 期望 200", status)
	}
	entryMaps := m3DexEntryMaps(t, raw)
	byID := make(map[string]map[string]any, len(entryMaps))
	for _, m := range entryMaps {
		if id, _ := m["id"].(string); id != "" {
			byID[id] = m
		}
	}
	unlocked := 0
	for _, e := range dex.Species {
		if e.Silhouette == "" {
			t.Errorf("dex[%s]: silhouette 为空", e.ID)
		} else {
			fetch(e.Silhouette, "dex["+e.ID+"].silhouette")
		}
		if !strings.HasPrefix(e.Silhouette, ossPetsPrefix) {
			t.Errorf("dex[%s]: silhouette = %q, 期望以 %q 开头（桶直链）", e.ID, e.Silhouette, ossPetsPrefix)
		}
		if e.Unlocked {
			unlocked++
			if len(e.Stages) != 3 {
				t.Errorf("dex[%s]: stages 长度 = %d, 期望 3（%v）", e.ID, len(e.Stages), e.Stages)
			}
			for i, u := range e.Stages {
				if u == "" {
					t.Errorf("dex[%s]: stages[%d] 为空 URL", e.ID, i)
					continue
				}
				if !strings.HasPrefix(u, ossPetsPrefix) {
					t.Errorf("dex[%s]: stages[%d] = %q, 期望以 %q 开头（桶直链）", e.ID, i, u, ossPetsPrefix)
				}
				fetch(u, "dex["+e.ID+"].stages["+string(rune('1'+i))+"]")
			}
			continue
		}
		// 锁定条目：stages 键必须显式 null（map 语义检查，键缺失/非 null 均违规）。
		m, ok := byID[e.ID]
		if !ok {
			t.Errorf("dex[%s]: 锁定条目未出现在 map 视图中", e.ID)
			continue
		}
		if v, has := m["stages"]; !has {
			t.Errorf("dex[%s]: 缺少 stages 键, 期望显式 null", e.ID)
		} else if v != nil {
			t.Errorf("dex[%s]: stages = %v, 期望 null（未解锁不得泄露立绘 URL）", e.ID, v)
		}
	}
	// c) 至少一个解锁物种，否则上述 stages 取回断言未真正生效。
	if unlocked == 0 {
		t.Fatalf("adopt 后 dex 无解锁条目，断言失效")
	}

	// b) eggs：全部 imageUrl 可取回。
	resp, body := doJSON(t, h, http.MethodGet, "/api/eggs", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/eggs 状态码 = %d, 期望 200, body=%v", resp.StatusCode, body)
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("重新编码 eggs 响应失败: %v", err)
	}
	var typed ossEggsResp
	if err := json.Unmarshal(rawBody, &typed); err != nil {
		t.Fatalf("GET /api/eggs 响应解码失败: %v", err)
	}
	if len(typed.Eggs) != 6 {
		t.Fatalf("eggs 数量 = %d, 期望恰好 6", len(typed.Eggs))
	}
	for _, e := range typed.Eggs {
		if e.ImageURL == "" {
			t.Errorf("eggs[%s]: imageUrl 为空", e.ID)
			continue
		}
		fetch(e.ImageURL, "eggs["+e.ID+"].imageUrl")
	}
}
