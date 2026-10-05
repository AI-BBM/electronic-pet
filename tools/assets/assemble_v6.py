#!/usr/bin/env python3
"""#37 v6 装配：原图保底帧序列 → 遮罩外差值自检 → 200ms WebP。

帧序列（8 帧 200ms，黄总 v6 工艺③：2-3 关键位+镜像组合）：
  f0 = 原图（站姿，零生成）
  f1 = pose1（关键位 A：耳/翅内摆/挥左）
  f2 = pose2（关键位 B：耳/翅外摆/挥右）
  f3 = mirror(f1)   f4 = pose1   f5 = mirror(f2)   f6 = pose2
  f7 = 原图（归位，与 f0 同 → 循环闭合）
镜像帧是"同一生成关键位帧的水平翻转"——挥手换边语义；非重新生成。

自检（工艺④）：逐帧与原图在遮罩外区域的平均像素差，须 <2/255；
镜像帧遮罩外与原图差异=0（同源像素翻转后身体居中对称区差异按实测计）。
注意：镜像帧翻转的是整帧，遮罩外身体区域非严格对称，故自测值实测实报。

产出：{out}/{pet}-anim.webp + wave_report_v6.json（逐帧来源/自测值）+ preview。
"""
import argparse
import json
import os
import sys

from PIL import Image, ImageChops, ImageStat

BASE = r"C:/Users/hxx/AppData/Local/Temp/m9v6"
OUT = os.path.join(BASE, "out")
MS = 200
MAX_BYTES = 300 * 1024
OUTSIDE_LIMIT = 2.0


def outside_diff(frame: Image.Image, orig: Image.Image, outside_mask: Image.Image):
    dd = ImageChops.difference(frame.convert("RGB"), orig.convert("RGB"))
    bucket = Image.new("RGB", dd.size, (0, 0, 0))
    bucket.paste(dd, (0, 0), outside_mask)
    return sum(ImageStat.Stat(bucket).mean) / 3.0


def mirror_inside(frame: Image.Image, orig: Image.Image, mask: Image.Image):
    """遮罩内镜像：仅翻转运动包络内的像素，遮罩外严格保留原图像素。

    整帧翻转会把脸/身体一起镜像（遮罩外差 21/255），violates v6 ④；
    挥手换边语义只发生在包络内，故镜像域限定在遮罩内。
    """
    flipped = frame.transpose(Image.FLIP_LEFT_RIGHT)
    out = orig.copy()
    out.paste(flipped, (0, 0), mask)
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pet", required=True, help="如 bunny_1")
    args = ap.parse_args()
    pet = args.pet

    orig = Image.open(os.path.join(BASE, f"{pet}_white.png")).convert("RGB")
    mask = Image.open(os.path.join(BASE, f"{pet}_mask.png")).convert("L")
    outside = mask.point(lambda v: 0 if v >= 128 else 255)  # 遮罩外=白
    cand = {}
    for pose_key in ("pose1", "pose2"):
        for seed in (68, 101):
            p = os.path.join(OUT, f"{pet}_{pose_key}_s{seed}.png")
            if os.path.exists(p):
                cand[(pose_key, seed)] = Image.open(p).convert("RGB")

    # 挑 seed：遮罩外差值最小者优先（理论上都≈0，取最稳）
    def pick(pose_key):
        best = min((s for (k, s) in cand if k == pose_key),
                   key=lambda s: outside_diff(cand[(pose_key, s)], orig, outside))
        return cand[(pose_key, best)], best

    f1, s1 = pick("pose1")
    f2, s2 = pick("pose2")

    frames = [orig.copy(), f1, f2,
              mirror_inside(f1, orig, mask), f1.copy(),
              mirror_inside(f2, orig, mask), f2.copy(),
              orig.copy()]
    sources = ["orig", f"pose1_s{s1}", f"pose2_s{s2}",
               f"mirror-inside(pose1_s{s1})", f"pose1_s{s1}",
               f"mirror-inside(pose2_s{s2})", f"pose2_s{s2}", "orig"]

    report = {"pet": pet, "ms": MS, "sources": sources, "outside_diffs": []}
    ok = True
    for i, (fr, src) in enumerate(zip(frames, sources)):
        v = outside_diff(fr, orig, outside)
        report["outside_diffs"].append(round(v, 3))
        mark = "PASS" if v < OUTSIDE_LIMIT else "FAIL"
        if v >= OUTSIDE_LIMIT:
            ok = False
        print(f"f{i} [{src}] outside-diff={v:.3f}/255 {mark}")
    if not ok:
        print("ERROR: 遮罩外差值超限", file=sys.stderr)
        return 2

    # 透明化 + 统一高度
    w, h = frames[0].size
    target_h = 512
    scale = min(1.0, target_h / h)
    if scale < 1.0:
        frames = [f.resize((int(w * scale), int(h * scale)), Image.LANCZOS)
                  for f in frames]
        orig = orig.resize((int(w * scale), int(h * scale)), Image.LANCZOS)
    # 白底→透明（管线同 v5 口径：255-min(R,G,B) + 泛洪，此处简化为闭运算版）
    from PIL import ImageDraw, ImageFilter
    rgba_frames = []
    for fr in frames:
        r, g, b = fr.split()[:3]
        mn = ImageChops.darker(ImageChops.darker(r, g), b)
        dist = ImageChops.invert(mn)
        near = dist.point(lambda v: 255 if v < 26 else 0)
        ff = near.copy()
        W, H = ff.size
        for xy in ((0,0),(W-1,0),(0,H-1),(W-1,H-1),(W//2,0),(W//2,H-1)):
            if ff.getpixel(xy) == 255:
                ImageDraw.floodfill(ff, xy, 128)
        bgm = ff.point(lambda v: 255 if v == 128 else 0)
        fg = ImageChops.invert(bgm)
        core = dist.point(lambda v: 255 if v >= 26 else 0)
        m = ImageChops.multiply(fg, core).filter(
            ImageFilter.MaxFilter(5)).filter(ImageFilter.MinFilter(5))
        out = fr.convert("RGBA")
        out.putalpha(m.filter(ImageFilter.GaussianBlur(1.5)))
        rgba_frames.append(out)

    out_path = os.path.join(OUT, f"{pet}-anim.webp")
    q = 85
    while True:
        rgba_frames[0].save(out_path, save_all=True,
                            append_images=rgba_frames[1:], duration=MS, loop=0,
                            format="WEBP", quality=q, method=4)
        size = os.path.getsize(out_path)
        if size <= MAX_BYTES or q <= 55:
            break
        q -= 8
    report["webp"] = {"path": out_path, "bytes": size, "quality": q}
    print(f"written {out_path}: {size} bytes (q={q})")
    with open(os.path.join(OUT, f"wave_report_v6_{pet}.json"), "w",
              encoding="utf-8") as fp:
        json.dump(report, fp, ensure_ascii=False, indent=1)

    sheet = Image.new("RGB", (frames[0].width * 8 + 70, frames[0].height + 10),
                      (255, 255, 255))
    x = 5
    for fr in frames:
        sheet.paste(fr, (x, 5))
        x += fr.width + 10
    sheet.save(os.path.join(OUT, f"preview_v6_{pet}.png"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
