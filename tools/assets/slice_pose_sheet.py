#!/usr/bin/env python3
"""姿态表切片（#44 管线第 3 步后半）：4×4 姿态表 → SR → 16 张 512×512 单帧。

#44 口径：RealESRGAN_x2plus → 2048×2048 → 每格内缩 1.2% 边距 → 切 16 张
512×512。输入是 gen_pose_sheet_16.py 的产出（1024×1024 表），先超分到
2048 再切。1.2% 内缩（每边约 6px @512）去格线/溢边后缩回 512。

用法：
  python slice_pose_sheet.py --sheet design/pose_sheets/bunny_3/sheet16_s68.png \
      --dst design/pose_sheets/bunny_3/slices [--tmp-dir <SR中转目录>]
产出：{dst}/pose{01..16}.png（已按动作 id 顺序）
"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import upscale_sr

INSET = 0.012  # 每边内缩比例（#44：1.2% 边距）


def main():
    ap = argparse.ArgumentParser(description="4×4 姿态表 → SR → 16×512 切片")
    ap.add_argument("--sheet", required=True, help="1024×1024 姿态表 PNG")
    ap.add_argument("--dst", required=True, help="切片输出目录")
    ap.add_argument("--tmp-dir", default=None, help="SR 中转目录（默认 dst/_sr）")
    args = ap.parse_args()

    from PIL import Image
    if not os.path.exists(args.sheet):
        print("错误：缺姿态表", args.sheet, file=sys.stderr)
        return 2

    tmp_dir = args.tmp_dir or os.path.join(args.dst, "_sr")
    sr_list = upscale_sr.run_one(args.sheet, tmp_dir, 2)
    big = Image.open(sr_list[0]).convert("RGB")
    if big.size != (2048, 2048):
        big = big.resize((2048, 2048), Image.LANCZOS)

    os.makedirs(args.dst, exist_ok=True)
    cell = 2048 // 4
    inset = round(cell * INSET)
    names = [f"pose{i:02d}.png" for i in range(1, 17)]
    for idx, name in enumerate(names):
        row, col = divmod(idx, 4)
        box = (col * cell + inset, row * cell + inset,
               (col + 1) * cell - inset, (row + 1) * cell - inset)
        crop = big.crop(box).resize((512, 512), Image.LANCZOS)
        crop.save(os.path.join(args.dst, name))
        print("slice", name, flush=True)
    print("ALL-DONE", args.dst)
    return 0


if __name__ == "__main__":
    sys.exit(main())
