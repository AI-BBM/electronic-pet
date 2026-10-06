#!/usr/bin/env python3
"""16 帧成品装配（#44 管线第 3 步收尾）：动作帧 → 抠透明 → 锚定定标 → 512×512
→ 200ms WebP（≤300KB，超限自动降质量）。

工艺沿用 M9（assemble_wave/v6）：白底泛洪抠透明（白绒毛主体用低阈值）+
脚底锚定（bbox 底边统一落到地面线）+ 宽度定标（按 bbox 宽缩放，姿态高度
变化不污染体型）。已知简化：腾空帧（09/10）按脚底锚定会贴地，腾空感靠
10 号姿态本身表达；如黄总要真腾空再加逐帧 y 偏移 meta。

用法：
  python assemble_webp_16.py --dir design/actions_raw/bunny_3 --seed 68 \
      [--out design/actions_raw/bunny_3/anim]
产出：{out}/pose{01..16}.png（512×512 透明）+ anim_16f_200ms.webp
      + assemble_report.json（逐帧 bbox/缩放/质量/体积）
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_actions_16 import DEFAULT_ACTIONS
import upscale_sr

CANVAS = 512
GROUND_Y = 468          # 地面线（脚底统一落此）
TARGET_W = 380          # 主体 bbox 目标宽（留呼吸边）
MAX_KB = 300


def matte(src, dst, white_t):
    """白底泛洪抠透明：四角种子泛洪白色→品红键色→键色转 alpha。"""
    from PIL import Image, ImageDraw
    im = Image.open(src).convert("RGB")
    w, h = im.size
    pad = 4
    big = Image.new("RGB", (w + pad * 2, h + pad * 2), (255, 255, 255))
    big.paste(im, (pad, pad))
    KEY = (255, 0, 255)
    for seed in [(0, 0), (big.size[0] - 1, 0), (0, big.size[1] - 1),
                 (big.size[0] - 1, big.size[1] - 1)]:
        ImageDraw.floodfill(big, seed, KEY, thresh=white_t)
    rgba = big.convert("RGBA")
    px = rgba.load()
    n = 0
    for y in range(big.size[1]):
        for x in range(big.size[0]):
            if px[x, y][:3] == KEY:
                px[x, y] = (0, 0, 0, 0)
                n += 1
    if n == 0:
        raise RuntimeError("泛洪未命中（阈值过小或非白底）: " + src)
    rgba = rgba.crop((pad, pad, pad + w, pad + h))
    rgba.save(dst)
    return rgba


def despeckle(rgba, min_ratio=0.02):
    """清除透明化后的孤立不透明碎块（耳机线浮段/背景残口袋）。

    保留最大连通域与面积 ≥最大域 min_ratio 的贴身部件（如磨损线缆），
    其余碎块 alpha 清零。1/4 尺度 BFS 连通域，足够定位碎块。"""
    from PIL import Image
    w, h = rgba.size
    small = rgba.resize((w // 4, h // 4), Image.NEAREST)
    px = small.load()
    sw, sh = small.size
    seen = [[False] * sw for _ in range(sh)]
    comps = []
    for sy in range(sh):
        for sx in range(sw):
            if seen[sy][sx] or px[sx, sy][3] == 0:
                seen[sy][sx] = True
                continue
            stack, cells = [(sx, sy)], []
            seen[sy][sx] = True
            while stack:
                x, y = stack.pop()
                cells.append((x, y))
                for nx, ny in ((x+1, y), (x-1, y), (x, y+1), (x, y-1)):
                    if 0 <= nx < sw and 0 <= ny < sh and not seen[ny][nx] \
                            and px[nx, ny][3] != 0:
                        seen[ny][nx] = True
                        stack.append((nx, ny))
            comps.append(cells)
    if not comps:
        return rgba
    biggest = max(len(c) for c in comps)
    keep = set()
    for c in comps:
        if len(c) >= biggest * min_ratio:
            keep.update(c)
    mask = Image.new("L", small.size, 0)
    mp = mask.load()
    for x, y in keep:
        mp[x, y] = 255
    mask = mask.resize(rgba.size, Image.NEAREST)
    r, g, b, a = rgba.split()
    from PIL import ImageChops
    return Image.merge("RGBA", (r, g, b, ImageChops.multiply(a, mask)))


def frame_512(rgba, scale):
    """抠图后：全局统一 scale 定标（防帧间抖动）→ 脚底锚定 → 512×512 画布。"""
    from PIL import Image
    bbox = rgba.getbbox()
    if not bbox:
        raise RuntimeError("全透明帧")
    w, h = bbox[2] - bbox[0], bbox[3] - bbox[1]
    nw, nh = max(1, round(w * scale)), max(1, round(h * scale))
    subj = rgba.crop(bbox).resize((nw, nh), Image.LANCZOS)
    canvas = Image.new("RGBA", (CANVAS, CANVAS), (0, 0, 0, 0))
    cx = (CANVAS - nw) // 2
    cy = GROUND_Y - nh
    if cy < 4 or cx < 0:
        raise RuntimeError(f"主体超画布 bbox={w}x{h} scale={scale:.3f}")
    canvas.paste(subj, (cx, cy), subj)
    return canvas, {"bbox": list(bbox), "w": w, "h": h}


def main():
    ap = argparse.ArgumentParser(description="16 帧 → 抠透明锚定 → 200ms WebP")
    ap.add_argument("--dir", required=True, help="动作帧目录（{action}_s{seed}.png）")
    ap.add_argument("--seed", required=True, type=int)
    ap.add_argument("--out", default=None)
    ap.add_argument("--white-t", type=int, default=14,
                    help="泛洪阈值：白/米绒毛主体 14，奶油色默认 26（M9 经验）")
    args = ap.parse_args()

    out = args.out or os.path.join(args.dir, "anim")
    os.makedirs(out, exist_ok=True)
    tmp_dir = os.path.join(out, "_matte")
    os.makedirs(tmp_dir, exist_ok=True)

    actions = list(DEFAULT_ACTIONS)
    mattes = {}
    for action in actions:
        m_path = os.path.join(tmp_dir, f"{action}_m.png")
        if os.path.exists(m_path):
            from PIL import Image
            mattes[action] = Image.open(m_path).convert("RGBA")
            print("matte(cached)", action, flush=True)
            continue
        src = os.path.join(args.dir, f"{action}_s{args.seed}.png")
        if not os.path.exists(src):
            print("错误：缺帧", src, file=sys.stderr)
            return 2
        sr_list = upscale_sr.run_one(src, tmp_dir, 2)   # 640×1024 → 1280×2048
        mattes[action] = matte(sr_list[0], m_path, args.white_t)
        print("matte", action, flush=True)
    mattes = {a: despeckle(m) for a, m in mattes.items()}

    # 全局统一定标：所有帧同一个 scale（min of 宽定标/高上限），姿势高差真实保留
    MAX_H = GROUND_Y - 28          # 顶部留 28px
    scale = min(min(TARGET_W / (b[2] - b[0]), MAX_H / (b[3] - b[1]))
                for b in (m.getbbox() for m in mattes.values()))

    frames, report = [], {"frames": {}, "white_t": args.white_t,
                          "canvas": CANVAS, "ground_y": GROUND_Y,
                          "target_w": TARGET_W, "global_scale": round(scale, 5),
                          "fps_ms": 200}
    for idx, action in enumerate(actions, start=1):
        canvas, info = frame_512(mattes[action], scale)
        name = f"pose{idx:02d}.png"
        canvas.save(os.path.join(out, name))
        frames.append(canvas)
        report["frames"][name] = {"action": action, **info}
        print("frame", name, action, flush=True)

    path = os.path.join(out, "anim_16f_200ms.webp")
    quality = 85
    while quality >= 45:
        frames[0].save(path, save_all=True, append_images=frames[1:],
                       duration=200, loop=0, method=6, quality=quality)
        kb = os.path.getsize(path) // 1024
        if kb <= MAX_KB:
            break
        quality -= 10
    report["webp"] = {"path": path, "kb": kb, "quality": quality,
                      "frames": len(frames), "within_limit": kb <= MAX_KB}
    with open(os.path.join(out, "assemble_report.json"), "w",
              encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=1)
    print(f"WEBP {path} {kb}KB q={quality} frames={len(frames)}", flush=True)
    if kb > MAX_KB:
        print("错误：超 300KB 体积上限", file=sys.stderr)
        return 3
    print("ALL-DONE", out)
    return 0


if __name__ == "__main__":
    sys.exit(main())
