#!/usr/bin/env python3
"""M9 idle 动画生成 v2：静态宠物图 → 明显可见的蹦跳/摇摆帧动画（动态 WebP）。

动作设计（#37 返工标准 R1/R2）：
- 整身垂直蹦跳（hop）：主形体垂直位移 ≥6% 画布高
- 左右摇摆（sway）：主形体水平位移 ≥6% 画布宽
- 以底部中心为轴的轻微旋转摆动，强化"活物"感
- 帧间平均像素差（连续帧 + 首尾闭合帧）自检 ≥15/255，不达标自动放大振幅重试（至多 3 轮）

用法：
  python make_idle_anim.py --src base.png --out anim.webp
      [--frames 8] [--ms 110] [--quality 80] [--min-diff 15]

验收锚点（Issue #37 返工标准）：帧间平均差 ≥15/255、单文件 ≤300KB、循环自然。
"""
import argparse
import math
import os
import sys

from PIL import Image, ImageChops, ImageStat


def frame_diff(a: Image.Image, b: Image.Image) -> float:
    """两帧全图 RGB 平均绝对差（0-255，不透明加权口径与 PM 复测一致）。"""
    d = ImageChops.difference(a.convert("RGB"), b.convert("RGB"))
    st = ImageStat.Stat(d)
    means = st.mean  # R,G,B
    return sum(means) / 3.0


def build_frame(base: Image.Image, phase: float, sway_px: float,
                bounce_px: float, rot_deg: float, w: int, h: int) -> Image.Image:
    rot = base.rotate(rot_deg, resample=Image.BICUBIC, center=(w / 2, h - 2))
    out = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    out.paste(rot, (int(round(sway_px)), int(round(-bounce_px))), rot)
    return out


def tri(f: int, frames: int) -> float:
    """三角波：-1 → +1 → -1 线性往返（匀速，相邻帧位移恒定）。"""
    p = (f % frames) / frames
    return 4.0 * p - 1.0 if p < 0.5 else 3.0 - 4.0 * p


def gen_frames(base: Image.Image, frames: int, sway: float, bounce: float,
               rot: float) -> list:
    """三角波匀速摇摆+蹦跳（#37 返工 R1：每对相邻帧位移恒定且明显）。"""
    w, h = base.size
    out = []
    for f in range(frames):
        s = tri(f, frames)          # -1..+1
        dx = sway * s
        dy = -abs(bounce * s)       # 上下各半程（|s| 0→1→0）
        dr = rot * s
        out.append(build_frame(base, f, dx, dy, dr, w, h))
    return out


def min_consecutive_diff(frames: list) -> float:
    n = len(frames)
    vals = [frame_diff(frames[i], frames[(i + 1) % n]) for i in range(n)]
    return min(vals)


def main():
    ap = argparse.ArgumentParser(description="静态宠物图 → 明显可见的 idle 帧动画 WebP")
    ap.add_argument("--src", required=True, help="基准 RGBA PNG")
    ap.add_argument("--out", required=True, help="输出动态 WebP 路径")
    ap.add_argument("--frames", type=int, default=8, help="帧数（默认 8）")
    ap.add_argument("--ms", type=int, default=110, help="每帧时长毫秒（默认 110）")
    ap.add_argument("--quality", type=int, default=80, help="WebP 质量（默认 80）")
    ap.add_argument("--min-diff", type=float, default=15.0,
                    help="帧间平均差下限 0-255（默认 15，#37 R1）")
    args = ap.parse_args()

    base = Image.open(args.src).convert("RGBA")
    w, h = base.size
    sway = max(18.0, round(w * 0.06))      # ≥6% 画布宽
    bounce = max(18.0, round(h * 0.06))    # ≥6% 画布高
    rot = 6.0                              # 底部为轴 ±6°
    frames_n = args.frames

    max_sway = w * 0.30
    max_bounce = h * 0.30
    max_rot = 25.0
    scale = 1.0
    ok = False
    for attempt in range(5):
        frames = gen_frames(base, frames_n,
                            min(sway * scale, max_sway),
                            min(bounce * scale, max_bounce),
                            min(rot * scale, max_rot))
        md = min_consecutive_diff(frames)
        print(f"attempt {attempt + 1}: sway={min(sway * scale, max_sway):.0f}px "
              f"bounce={min(bounce * scale, max_bounce):.0f}px "
              f"rot={min(rot * scale, max_rot):.1f}° min-consecutive-diff={md:.1f}/255")
        if md >= args.min_diff:
            ok = True
            break
        scale *= 1.8
    if not ok:
        print("ERROR: 5 轮放大后仍不达标", file=sys.stderr)
        return 2

    seq = frames  # 三角波 8 帧首尾自然衔接（相邻位移恒定），无需追加重复帧
    seq[0].save(
        args.out, save_all=True, append_images=seq[1:], duration=args.ms,
        loop=0, format="WEBP", quality=args.quality, method=4,
    )
    size = os.path.getsize(args.out)
    print(f"written {args.out}: {len(seq)} frames, {size} bytes")
    if size > 300 * 1024:
        print("WARNING: 超过 300KB 验收线，建议降 quality", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
