#!/usr/bin/env python3
"""M9 idle 动画生成：由现有静态宠物图生成帧动画（动态 WebP）。

原理（#37 试点）：对基准 RGBA 图做逐行位移的确定性形变——脚部锚定不动、
越靠上振幅越大（呼吸起伏 + 耳朵/头部轻微摆动），按时间相位展开成 N 帧，
帧间像素同源（同一张图的形变），轮廓一致性由构造保证。
输出动态 WebP（透明背景、无限循环）。

用法：
  python make_idle_anim.py --src base.png --out anim.webp
      [--frames 8] [--ms 110] [--amp 6] [--quality 80]

验收锚点（Issue #37）：单文件 ≤300KB、循环自然、透明背景。
"""
import argparse
import math
import sys

from PIL import Image


def make_frames(src: Image.Image, frames: int, amp: float):
    """按行相位正弦形变生成 frames 帧（返回 RGBA Image 列表）。"""
    src = src.convert("RGBA")
    w, h = src.size
    px = src.load()
    out = []
    for f in range(frames):
        t = f / frames
        im = Image.new("RGBA", (w, h), (0, 0, 0, 0))
        dst = im.load()
        # 每目标行 y 的采样源行：振幅随高度从脚部 0 线性放大到顶部 amp
        offsets = [
            amp * ((h - y) / h) ** 1.5 * math.sin(2 * math.pi * (t + 0.12 * (y / h)))
            for y in range(h)
        ]
        for y in range(h):
            dy = offsets[y]
            f0 = int(dy)
            frac = dy - f0
            src_y0 = min(max(y, 0), h - 1)
            src_y1 = min(max(y + (1 if dy >= 0 else -1), 0), h - 1)
            for x in range(w):
                p0 = px[x, src_y0]
                p1 = px[x, src_y1]
                a0 = p0[3] / 255.0 * (1 - frac)
                a1 = p1[3] / 255.0 * frac
                if a0 + a1 <= 0:
                    dst[x, y] = (0, 0, 0, 0)
                    continue
                r = int((p0[0] * a0 + p1[0] * a1) / (a0 + a1))
                g = int((p0[1] * a0 + p1[1] * a1) / (a0 + a1))
                b = int((p0[2] * a0 + p1[2] * a1) / (a0 + a1))
                dst[x, y] = (r, g, b, int((a0 + a1) * 255))
        out.append(im)
    return out


def main():
    ap = argparse.ArgumentParser(description="静态宠物图 → idle 帧动画 WebP")
    ap.add_argument("--src", required=True, help="基准 RGBA PNG")
    ap.add_argument("--out", required=True, help="输出动态 WebP 路径")
    ap.add_argument("--frames", type=int, default=8, help="帧数（默认 8）")
    ap.add_argument("--ms", type=int, default=110, help="每帧时长毫秒（默认 110）")
    ap.add_argument("--amp", type=float, default=6.0, help="顶部最大位移像素（默认 6）")
    ap.add_argument("--quality", type=int, default=80, help="WebP 质量（默认 80）")
    args = ap.parse_args()

    src = Image.open(args.src).convert("RGBA")
    frames = make_frames(src, args.frames, args.amp)
    frames[0].save(
        args.out, save_all=True, append_images=frames[1:], duration=args.ms,
        loop=0, format="WEBP", quality=args.quality, method=4,
    )
    import os
    size = os.path.getsize(args.out)
    print(f"written {args.out}: {len(frames)} frames, {size} bytes")
    if size > 300 * 1024:
        print("WARNING: 超过 300KB 验收线，建议降 quality 或缩尺寸", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
