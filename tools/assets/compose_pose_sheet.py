#!/usr/bin/env python3
"""姿态表合成：16 张单帧 → 4×4 带编号姿态表（1024×1024）。

gen_pose_sheet_16.py 的单张整表生成路线经两轮探针确认形象漂移（猴脸/灯丢/
编号幻觉，样张存 design/pose_sheets/bunny_3/ 作证据），废弃；产线改为
gen_actions_16.py 逐帧生成（同 seed 列形象稳）+ 本脚本程序合成——编号用
PIL 绘制，永不幻觉；逐格审、切片、装配都受益于单帧即成品。

用法：
  python compose_pose_sheet.py --dir design/actions_raw/bunny_3 --seed 68 \
      [--out design/pose_sheets/bunny_3/sheet16_composed.png]
帧文件名约定：{action}_s{seed}.png，action 顺序 = gen_actions_16.DEFAULT_ACTIONS。
缺帧时报错点名（不静默跳格）。
"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_actions_16 import DEFAULT_ACTIONS


def main():
    ap = argparse.ArgumentParser(description="16 单帧 → 4×4 编号姿态表")
    ap.add_argument("--dir", required=True, help="动作帧目录")
    ap.add_argument("--seed", required=True, type=int, help="选用同一 seed 列")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()

    from PIL import Image, ImageDraw, ImageFont
    cell = 256
    pad = 2
    W = H = 4 * (cell + pad) + pad
    sheet = Image.new("RGB", (W, H), (255, 255, 255))
    draw = ImageDraw.Draw(sheet)
    try:
        font = ImageFont.truetype("arial.ttf", 30)
    except OSError:
        font = ImageFont.load_default()

    for idx, action in enumerate(DEFAULT_ACTIONS):
        row, col = divmod(idx, 4)
        path = os.path.join(args.dir, f"{action}_s{args.seed}.png")
        if not os.path.exists(path):
            print("错误：缺帧", path, file=sys.stderr)
            return 2
        im = Image.open(path).convert("RGB").resize((cell, cell), Image.LANCZOS)
        x = pad + col * (cell + pad)
        y = pad + row * (cell + pad)
        sheet.paste(im, (x, y))
        draw.rectangle([x, y, x + 34, y + 34], fill=(255, 255, 255))
        draw.text((x + 6, y + 2), str(idx + 1), fill=(0, 0, 0), font=font)

    out = args.out or os.path.join(args.dir, f"sheet16_composed_s{args.seed}.png")
    sheet.save(out)
    print("sheet ok", out, sheet.size)
    return 0


if __name__ == "__main__":
    sys.exit(main())
