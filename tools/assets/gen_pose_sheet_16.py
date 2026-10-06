#!/usr/bin/env python3
"""16 格动作姿态表生成（#44 管线第 2 步）：定稿角色卡 → 单张 4×4 带编号动作表。

**产线状态（2026-10-06）：本脚本单张整表路线已废弃**——bunny_3 两轮探针
（sheet16_s68/s101）形象漂移严重（脸型走猴、耳内灯丢、编号幻觉 12/3 错乱），
样张保留在 design/pose_sheets/bunny_3/ 作证据。产线改用组合路线：
gen_actions_16.py 逐帧生成（同 seed 列形象稳）+ compose_pose_sheet.py 程序
合成编号表。本脚本保留作对照，勿用于交付。

generate-16-pose-sheet 规范：以定稿角色卡为单图参考，1:1 画布 4×4 共 16 格
带编号连贯动作，单图锁死时间轨迹与全身比例（PM 逐格审的对象就是这张表）。
动作清单直接复用 gen_actions_16.DEFAULT_ACTIONS（#44 定稿冻结版）——两处
共用单一事实源，禁止在两脚本里各写一份。

提示词注意：数字编号必需，故负面词不带 text/multiple characters（与
gen_character_card 的 NEGATIVE 不同源，勿改回）。

用法（本机 ComfyUI 8188）：
  python gen_pose_sheet_16.py --species bunny --stage 3 \
      --card design/character_cards/bunny_3/card_front_img2img_s68.png \
      [--seeds 68,101]
产出：design/pose_sheets/{species}_{stage}/sheet16_s{seed}.png + meta.json
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import upscale_sr
from gen_character_card import APPEARANCE_V2, build_graph, queue_and_wait, fetch_png
from gen_actions_16 import DEFAULT_ACTIONS

NEGATIVE_SHEET = ("background scene, gradient background, colored background, "
                  "floor shadow, deformed, extra limbs, blurry, watermark, signature")

GRID_SPEC = ("character pose timeline sheet, the exact same character repeated 16 "
             "times in a clean 4x4 grid of equal square cells, rows read left to "
             "right top to bottom, each cell has one small neat digit number from "
             "1 to 16 in its top-left corner, the 16 numbered poses form one "
             "continuous action timeline: ")


def sheet_prompt(key):
    appearance = APPEARANCE_V2.get(key)
    if appearance is None:
        raise SystemExit(f"错误：APPEARANCE_V2 无 {key} 基准描述，先补表再出表")
    seq = " ; ".join(f"{i} {en}" for i, (_, en)
                     in enumerate(DEFAULT_ACTIONS.values(), start=1))
    return (appearance + ", " + GRID_SPEC + seq +
            "; identical character size and proportions in every cell, evenly "
            "aligned rows and columns, crisp pure white background, high detail")


def main():
    ap = argparse.ArgumentParser(description="16 格动作姿态表生成（卡→4×4 编号表）")
    ap.add_argument("--species", required=True)
    ap.add_argument("--stage", required=True, type=int, choices=[1, 2, 3])
    ap.add_argument("--card", required=True, help="已过审定稿角色卡 PNG")
    ap.add_argument("--seeds", default="68,101")
    ap.add_argument("--out-dir", default="design/pose_sheets")
    args = ap.parse_args()

    key = (args.species, args.stage)
    prompt = sheet_prompt(key)  # 无 V2 表直接报错退出
    if not os.path.exists(args.card):
        print("错误：缺定稿卡", args.card, file=sys.stderr)
        return 2

    ref_name = upscale_sr.upload_image(args.card)["name"]
    print("card uploaded:", ref_name, flush=True)

    out_dir = os.path.join(args.out_dir, f"{args.species}_{args.stage}")
    os.makedirs(out_dir, exist_ok=True)
    meta_path = os.path.join(out_dir, "meta.json")
    meta = {}
    if os.path.exists(meta_path):
        with open(meta_path, encoding="utf-8") as f:
            meta = json.load(f)

    for seed in [int(s) for s in args.seeds.split(",")]:
        tag = f"sheet16_s{seed}"
        if tag in meta:
            print("skip existing", tag, flush=True)
            continue
        outs = queue_and_wait(build_graph(ref_name, prompt, NEGATIVE_SHEET, seed,
                                          width=1024, height=1024, denoise=1.0))
        fetch_png(outs, os.path.join(out_dir, tag + ".png"))
        meta[tag] = {"seed": seed, "prompt": prompt, "negative": NEGATIVE_SHEET,
                     "mode_gen": "EDIT denoise=1.0", "latent": "1024x1024",
                     "steps": 25, "cfg": 1, "sampler": "euler/simple",
                     "ref_card": args.card,
                     "actions": list(DEFAULT_ACTIONS.keys())}
        with open(meta_path, "w", encoding="utf-8") as fp:
            json.dump(meta, fp, ensure_ascii=False, indent=1)
        print("done", tag, flush=True)
    print("ALL-DONE", key)
    return 0


if __name__ == "__main__":
    sys.exit(main())
