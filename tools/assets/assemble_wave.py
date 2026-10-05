#!/usr/bin/env python3
"""#37 M9 v5 挥手帧装配：候选挑帧 → 脚底锚定对齐 → 白底抠透明 → 合成 WebP。

工艺（PM v5 ③④）：
- 挑帧：以 f0 基准对每帧候选打一致性分（对齐后整帧 RGB 平均差），推荐 MIN 帧；
  人工复核后用 --picks 覆盖（试点结论：单一 seed 整列取帧的帧间一致性最优）。
- 对齐：脚底锚定 + 宽度定标（抬爪拉高 bbox 但几乎不改变宽度；用高度定标会被
  抬爪污染导致身体缩小），水平对齐 bbox 中心，垂直对齐 bbox 底边——抬爪即纯
  向上运动；越界区填白（默认黑填充满进抠图掩码成伪影）。
- 抠透明：白距离 = 255-min(R,G,B)（亮度口径会把浅黄主体判成背景），近白区自
  四边中点泛洪判背景，羽化 1.5px；与主体不相连的亮灰碎块（地面阴影残余）清除。
- 镜像增强（--mirror）：左右挥幅不足时，挥摆帧取同源帧水平镜像（同一生成帧
  翻转，非重新生成），交付说明如实标注。
- 合成：8 帧 200ms 循环 WebP ≤300KB；帧差（白底合成口径）随报告输出，f7→f0
  为同姿循环闭合对（帧 0/7 同提示词同 seed，天然一致），单独列 motion_diffs_min。

用法：
  python assemble_wave.py --cand-dir <候选根目录> --species bunny --stage 1 \
      [--picks f0:68,...] [--mirror f3:f2,f5:f4] [--quality 85] [--diag]
产出：{cand-dir}/{species}_{stage}/{species}_{stage}-anim.webp
     + assemble_report.json + preview_sheet.png
"""
import argparse
import json
import os
import sys

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageStat

WHITE_T_DEFAULT = 26  # 近白背景阈值（255-min(R,G,B) 口径；白绒毛主体需调低）
SHADOW_DROP = 0.02    # 亮灰离群碎块面积阈值（占主体面积比例）
OUT_H = 512           # 成品帧高（与 OSS 静态图量级一致）
MS = 200           # 每帧毫秒（黄总 2026-10-05 反馈 110ms 过快，定版 200ms≈挥手周期 1.6s）
MAX_BYTES = 300 * 1024


def load_candidates(cand_dir, species, stage):
    d = os.path.join(cand_dir, f"{species}_{stage}")
    with open(os.path.join(d, "meta.json"), encoding="utf-8") as f:
        meta = json.load(f)
    frames = {}
    for tag, rec in meta.items():
        p = os.path.join(d, tag + ".png")
        if os.path.exists(p):
            frames.setdefault(rec["frame"], {})[rec["seed"]] = p
    return frames, d


def fg_mask(rgb: Image.Image, white_t: int = WHITE_T_DEFAULT):
    """主体掩码：非白区 - 泛洪可达近白区；L 模式 0/255。"""
    r, g, b = rgb.split()[:3]
    mn = ImageChops.darker(ImageChops.darker(r, g), b)
    dist = ImageChops.invert(mn)  # 白→0
    near_white = dist.point(lambda v: 255 if v < white_t else 0)
    ff = near_white.copy()
    w, h = ff.size
    for seed_xy in ((0, 0), (w - 1, 0), (0, h - 1), (w - 1, h - 1),
                    (w // 2, 0), (w // 2, h - 1)):
        if ff.getpixel(seed_xy) == 255:
            ImageDraw.floodfill(ff, seed_xy, 128)
    bg = ff.point(lambda v: 255 if v == 128 else 0)
    fg = ImageChops.invert(bg)
    core = dist.point(lambda v: 255 if v >= white_t else 0)
    m = ImageChops.multiply(fg, core)
    # 闭运算封白绒毛内部小洞（泛洪沿低阈值绒毛渗入），不移动外缘
    return m.filter(ImageFilter.MaxFilter(5)).filter(ImageFilter.MinFilter(5))


def subject_stats(mask: Image.Image):
    bbox = mask.getbbox()
    if not bbox:
        return None
    small = mask.resize((max(1, mask.width // 4), max(1, mask.height // 4)))
    sw, sh = small.size
    tot = sx = sy = 0
    pix = small.load()
    for y in range(sh):
        for x in range(sw):
            if pix[x, y] > 127:
                tot += 1
                sx += x
                sy += y
    centroid = ((sx / tot) * 4 if tot else 0, (sy / tot) * 4 if tot else 0)
    return dict(bbox=bbox, centroid=centroid, height=bbox[3] - bbox[1],
                width=bbox[2] - bbox[0], area=mask.histogram()[255])


def align_to(rgb: Image.Image, st, st0):
    """脚底锚定 + 宽度定标（详见模块 docstring）。"""
    scale = st0["width"] / st["width"]
    scale = max(0.90, min(1.12, scale))
    cx = (st["bbox"][0] + st["bbox"][2]) / 2
    cx0 = (st0["bbox"][0] + st0["bbox"][2]) / 2
    a = 1 / scale
    c = cx - a * cx0
    f_ = st["bbox"][3] - a * st0["bbox"][3]
    return rgb.transform(rgb.size, Image.AFFINE, (a, 0, c, 0, a, f_),
                         resample=Image.BICUBIC, fillcolor=(255, 255, 255)), scale


def drop_bright_splinters(mask: Image.Image, rgb: Image.Image, subject_area: int):
    """清除与主体不相连的亮灰碎块（地面阴影残余）。"""
    m = mask.copy()
    w, h = m.size
    pix = m.load()
    lbl = [[0] * w for _ in range(h)]
    cur = 0
    comps = {}
    for y in range(h):
        for x in range(w):
            if pix[x, y] > 127 and lbl[y][x] == 0:
                cur += 1
                stack = [(x, y)]
                lbl[y][x] = cur
                pxs = []
                while stack:
                    px, py = stack.pop()
                    pxs.append((px, py))
                    for nx, ny in ((px+1, py), (px-1, py), (px, py+1), (px, py-1)):
                        if 0 <= nx < w and 0 <= ny < h and lbl[ny][nx] == 0 \
                                and pix[nx, ny] > 127:
                            lbl[ny][nx] = cur
                            stack.append((nx, ny))
                comps[cur] = pxs
    if len(comps) <= 1:
        return mask
    main = max(comps.values(), key=len)
    dd = ImageDraw.Draw(m)
    rgbp = rgb.load()
    for pxs in comps.values():
        if pxs is main or len(pxs) >= subject_area * SHADOW_DROP:
            continue
        bright = sum(1 for px, py in pxs
                     if rgbp[px, py][0] > 185 and rgbp[px, py][1] > 185
                     and rgbp[px, py][2] > 185)
        if bright / len(pxs) > 0.6:
            for px, py in pxs:
                dd.point((px, py), 0)
    return m


def to_rgba(rgb: Image.Image, mask: Image.Image):
    out = rgb.convert("RGBA")
    out.putalpha(mask.filter(ImageFilter.GaussianBlur(1.5)))
    return out


def frame_diff(a: Image.Image, b: Image.Image) -> float:
    d = ImageChops.difference(a.convert("RGB"), b.convert("RGB"))
    return sum(ImageStat.Stat(d).mean) / 3.0


def compose_white(rgba: Image.Image) -> Image.Image:
    bg = Image.new("RGB", rgba.size, (255, 255, 255))
    bg.paste(rgba, mask=rgba.getchannel("A"))
    return bg


def main():
    ap = argparse.ArgumentParser(description="挥手帧装配：挑帧/对齐/抠透明/WebP")
    ap.add_argument("--cand-dir", required=True, help="候选帧根目录")
    ap.add_argument("--species", required=True, choices=["bunny", "chick"])
    ap.add_argument("--stage", required=True, type=int, choices=[1, 2, 3])
    ap.add_argument("--picks", help="手工挑帧 f0:68,f3:101（覆盖程序推荐）")
    ap.add_argument("--mirror", help="镜像增强 f3:f2,f5:f4（目标:源）")
    ap.add_argument("--white-t", type=int, default=WHITE_T_DEFAULT,
                    help="近白背景阈值（白绒毛主体如 bunny_3 建议调低到 14）")
    ap.add_argument("--quality", type=int, default=85)
    ap.add_argument("--diag", action="store_true", help="输出抠图掩码诊断图")
    args = ap.parse_args()

    frames, d = load_candidates(args.cand_dir, args.species, args.stage)
    picks = {}
    if args.picks:
        for kv in args.picks.split(","):
            k, v = kv.replace("f", "").split(":")
            picks[int(k)] = int(v)

    f0_seed = picks.get(0, sorted(frames[0])[len(frames[0]) // 2])
    rgb0 = Image.open(frames[0][f0_seed]).convert("RGB")
    st0 = subject_stats(fg_mask(rgb0, args.white_t))

    def mask_of(img):
        return fg_mask(img, args.white_t)
    report = {"species": args.species, "stage": args.stage, "f0_seed": f0_seed,
              "frames": []}

    chosen = {}
    for fr in range(8):
        cands = frames.get(fr)
        if not cands:
            print(f"错误：缺帧 {fr} 候选", file=sys.stderr)
            return 2
        scores = {}
        for seed, p in sorted(cands.items()):
            rgb = Image.open(p).convert("RGB")
            st = subject_stats(mask_of(rgb))
            if not st:
                scores[seed] = 999.0
                continue
            aligned, _ = align_to(rgb, st, st0)
            scores[seed] = round(frame_diff(aligned, rgb0), 1)
        best = picks.get(fr) or min(scores, key=scores.get)
        chosen[fr] = best
        report["frames"].append({"frame": fr, "seed": best,
                                 "cand_scores": scores})
        print(f"frame {fr}: seeds={scores} → pick s{best}", flush=True)

    rgba_frames = [None] * 8
    for fr in range(8):
        rgb = Image.open(frames[fr][chosen[fr]]).convert("RGB")
        st = subject_stats(mask_of(rgb))
        pre_shift = (round(st["centroid"][0] - st0["centroid"][0], 1),
                     round(st["centroid"][1] - st0["centroid"][1], 1))
        aligned, scale = align_to(rgb, st, st0)
        ma = mask_of(aligned)
        ma = drop_bright_splinters(ma, aligned, st["area"])
        if args.diag:
            diag = aligned.copy()
            tint = Image.new("RGB", diag.size, (0, 255, 255))
            diag.paste(tint, (0, 0), ma.point(lambda v: 120 if v > 127 else 0))
            diag.save(os.path.join(d, f"diag_mask_f{fr}.png"))
        rgba_frames[fr] = to_rgba(aligned, ma)
        report["frames"][fr].update({
            "pre_align_centroid_shift": pre_shift,
            "scale": round(scale, 4), "post_h": OUT_H})

    mirrors = {}
    if args.mirror:
        for kv in args.mirror.split(","):
            tgt, src = kv.replace("f", "").split(":")
            mirrors[int(tgt)] = int(src)
    for tgt, src in mirrors.items():
        rgba_frames[tgt] = rgba_frames[src].transpose(Image.FLIP_LEFT_RIGHT)
        report["frames"][tgt]["mirror_of"] = src
        print(f"frame {tgt} = mirror(f{src})")

    w, h = rgba_frames[0].size
    rgba_frames = [f.resize((int(w * OUT_H / h), OUT_H), Image.LANCZOS)
                   for f in rgba_frames]
    comp = [compose_white(f) for f in rgba_frames]
    diffs = [round(frame_diff(comp[i], comp[(i + 1) % 8]), 1) for i in range(8)]
    report["frame_diffs"] = diffs
    report["motion_diffs_min"] = min(diffs[:7])
    print("frame diffs:", diffs, "(f7→f0 为同姿循环闭合对)")

    out_path = os.path.join(d, f"{args.species}_{args.stage}-anim.webp")
    q = args.quality
    while True:
        rgba_frames[0].save(out_path, save_all=True, append_images=rgba_frames[1:],
                            duration=MS, loop=0, format="WEBP", quality=q, method=4)
        size = os.path.getsize(out_path)
        if size <= MAX_BYTES or q <= 55:
            break
        q -= 8
    report["webp"] = {"path": out_path, "bytes": size, "quality": q}
    print(f"written {out_path}: {size} bytes (q={q})")

    with open(os.path.join(d, "assemble_report.json"), "w", encoding="utf-8") as fp:
        json.dump(report, fp, ensure_ascii=False, indent=1)
    sheet = Image.new("RGB", (comp[0].width * 8 + 70, comp[0].height + 10),
                      (255, 255, 255))
    x = 5
    for c in comp:
        sheet.paste(c, (x, 5))
        x += c.width + 10
    sheet.save(os.path.join(d, "preview_sheet.png"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
