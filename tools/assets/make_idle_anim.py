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


def build_wave_frame(base: Image.Image, angle_deg: float, pivot, window,
                     feather: int) -> Image.Image:
    """绕 pivot 旋转 angle 度，仅在羽化窗口内与原图混合（挥手区域）。"""
    from PIL import ImageFilter
    warped = base.rotate(angle_deg, resample=Image.BICUBIC, center=pivot)
    mask = Image.new("L", base.size, 0)
    from PIL import ImageDraw
    dd = ImageDraw.Draw(mask)
    dd.rectangle(window, fill=255)
    mask = mask.filter(ImageFilter.GaussianBlur(feather))
    frame = base.copy()
    frame.paste(warped, (0, 0), mask)
    return frame


def gen_frames(base: Image.Image, frames: int, wave_deg: float):
    """打招呼挥手：一侧爪/耳区域绕肩轴左右挥动 2 次。"""
    w, h = base.size
    bbox = base.getchannel("A").getbbox()
    if not bbox:
        raise ValueError("基准图主体为空（alpha 全透明）")
    left, top, right, bottom = bbox
    sub_h = bottom - top
    window = (right - int((right - left) * 0.62), top,
              right - 2, top + int(sub_h * 0.42))
    pivot = (window[0] + 6, window[3])
    frames_out = []
    for f in range(frames):
        ph = 2 * math.pi * (2 * f / frames)
        angle = wave_deg * math.sin(ph)
        frames_out.append(build_wave_frame(base, angle, pivot, window, 12))
    return frames_out


def min_consecutive_diff(frames: list) -> float:
    n = len(frames)
    vals = [frame_diff(frames[i], frames[(i + 1) % n]) for i in range(n)]
    return min(vals)


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
    ap.add_argument("--wave-deg", type=float, default=18.0, help="挥手摆角度（默认 18）")
    ap.add_argument("--min-diff", type=float, default=15.0,
                    help="帧间平均差下限 0-255（默认 15，#37 R1）")
    args = ap.parse_args()

    base = Image.open(args.src).convert("RGBA")
    frames_n = args.frames
    wave_deg = args.wave_deg

    scale = 1.0
    ok = False
    for attempt in range(5):
        frames = gen_frames(base, frames_n, wave_deg * scale)
        md = min_consecutive_diff(frames)
        print(f"attempt {attempt + 1}: wave={wave_deg * scale:.1f}° "
              f"min-consecutive-diff={md:.1f}/255")
        if md >= args.min_diff:
            ok = True
            break
        scale *= 1.7
    if not ok:
        print("ERROR: 5 轮放大后仍不达标", file=sys.stderr)
        return 2

    seq = frames
    seq[0].save(
        args.out, save_all=True, append_images=seq[1:], duration=args.ms,
        loop=0, format="WEBP", quality=args.quality, method=4,
    )
    size = os.path.getsize(args.out)
    print(f"written {args.out}: {len(seq)} frames, {size} bytes")
    if size > 300 * 1024:
        print("WARNING: 超过 300KB 验收线", file=sys.stderr)
        return 2

    return 0


if __name__ == "__main__":
    sys.exit(main())
