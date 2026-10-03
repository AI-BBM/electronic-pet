package server_test

// M3 对抗审查回灌用例（M3-T14/T15）：
// - T14 抓「URL 存在 ≠ URL 可用」盲区：dex 返回的每个素材 URL 必须真实可取回且为图片；
//   相对路径经被测 handler 自取（内嵌资源恒验）；绝对 http(s) URL 需设 PET_SMOKE_OSS=1 才
//   外网取回（OSS 就绪前的默认跳过，避免单测依赖外网与桶策略进度）。
// - T15 加固 dex/wall 读与 points 写的并发同压（-race 保障）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestM3_AssetURLsFetchable (M3-T14) dex 全部素材 URL 可取回且 Content-Type 为图片。
func TestM3_AssetURLsFetchable(t *testing.T) {
	h := newHandler(t)
	token := mustJoin(t, h, "c1", "小明", "01")
	mustAdopt(t, h, token, "random") // 保证至少一个解锁物种

	status, resp, _ := m3GetDex(t, h, token)
	if status != http.StatusOK {
		t.Fatalf("GET /api/dex 状态码 = %d, 期望 200", status)
	}

	fetch := func(url string) {
		t.Helper()
		var resp *http.Response
		if strings.HasPrefix(url, "/") {
			req := httptest.NewRequest(http.MethodGet, url, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			resp = rec.Result()
		} else {
			if os.Getenv("PET_SMOKE_OSS") == "" {
				t.Skipf("外网素材 URL 检查需 PET_SMOKE_OSS=1（跳过 %s）", url)
			}
			r, err := http.Get(url)
			if err != nil {
				t.Errorf("GET %s 失败: %v", url, err)
				return
			}
			resp = r
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		ct := resp.Header.Get("Content-Type")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("素材 URL %s 状态码 = %d, 期望 200", url, resp.StatusCode)
		}
		if !strings.HasPrefix(ct, "image/") {
			t.Errorf("素材 URL %s Content-Type = %q, 期望 image/*（body 前 64 字节: %q）", url, ct, body)
		}
	}

	unlocked := 0
	for _, e := range resp.Species {
		if e.Silhouette == "" {
			t.Errorf("物种 %s silhouette 为空", e.ID)
			continue
		}
		fetch(e.Silhouette)
		if !e.Unlocked {
			continue
		}
		unlocked++
		if len(e.Stages) != 3 {
			t.Errorf("解锁物种 %s stages 长度 = %d, 期望 3", e.ID, len(e.Stages))
			continue
		}
		for _, u := range e.Stages {
			fetch(u)
		}
	}
	if unlocked == 0 {
		t.Fatalf("adopt 后 dex 无解锁条目，断言失效")
	}
}

// TestM3_DexWallConcurrentWithWrites (M3-T15) 读端（dex/wall）与写端（加分）并发同压，
// 在 -race 下验证无数据竞争、无 5xx（handlers 经 dbMu 串行化的结构性保障）。
func TestM3_DexWallConcurrentWithWrites(t *testing.T) {
	h := newHandler(t)

	const readers = 8
	const writers = 4
	const rounds = 10

	tokens := make([]string, writers)
	for i := range tokens {
		tokens[i] = mustJoin(t, h, "c1", "写手", string(rune('A'+i)))
		mustAdopt(t, h, tokens[i], "random")
	}

	var wg sync.WaitGroup
	errCh := make(chan int, (readers+writers)*rounds)

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				tok := tokens[j%writers]
				if st, _, _ := m3GetDex(t, h, tok); st >= 500 {
					errCh <- st
				}
				if st, _, _ := m3GetWall(t, h, tok, "?sort=points"); st >= 500 {
					errCh <- st
				}
				if st, _, _ := m3GetWall(t, h, tok, "?sort=recent"); st >= 500 {
					errCh <- st
				}
			}
		}(i)
	}
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				res := m2AddPoints(h, tokens[i], m2PointsRequest{
					Reason: "并发写", Value: 1, RequestID: "",
				})
				if res.Status >= 500 {
					errCh <- res.Status
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for st := range errCh {
		t.Errorf("并发同压出现 5xx: %d", st)
	}
}
