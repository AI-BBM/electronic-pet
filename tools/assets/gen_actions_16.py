#!/usr/bin/env python3
"""角色 16 动作生成（管线第 2 步）：角色卡 → 16 个动作关键帧。

形象锚 = gen_character_card.py 产出的定稿正视图卡（--card 指定选中 seed 的
卡）；每动作 = 卡参考 latent + 显式动作短语 + 固定 seed 列（M9 探针定案：
同 seed 列保脸，跨 seed 混挑必换形象）。动作图为白底关键帧，后续走
超分（upscale_sr）→ 抠图对齐（assemble 系）→ 8/16 帧循环装配或单帧素材。

动作清单（16 个，草案 v1——待 PM/黄总定稿后作为 DEFAULT_ACTIONS 冻结，
产品映射：wave 上线问候 / cheer+jump 被加分 / sad 被扣分 / sleep 离线 /
eat 喂食 / spin 升级进化 / bow 颁奖致谢）。

用法（本机 ComfyUI 8188）：
  python gen_actions_16.py --species bunny --stage 3 \
      --card design/character_cards/bunny_3/card_front_s68.png \
      [--actions jump,cheer,sleep,eat] [--seeds 68,101]
产出：design/actions_raw/{species}_{stage}/{action}_s{seed}.png + meta.json。
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_wave_frames import APPEARANCE
import upscale_sr
from gen_character_card import build_graph, queue_and_wait, fetch_png

HOST = upscale_sr.HOST
NEGATIVE = ("background scene, gradient background, colored background, floor shadow, "
            "multiple characters, deformed, extra limbs, blurry, text, watermark")

DEFAULT_SEEDS = [68, 101]

# 16 动作清单（Issue #44 定稿 v1，按黄总叙事「亮相→歪头→蓄力→挥手三连→蹦跳→大跳→收官」冻结）
DEFAULT_ACTIONS = {
    "01_opening":    ("开场亮相", "standing naturally and relaxed facing forward, establishing character identity"),
    "02_tilt_left":  ("律动歪头·左", "tilting head playfully to the left with big curious eyes"),
    "03_tilt_right": ("律动歪头·右", "tilting head cheerfully to the right with sparkling happy expression"),
    "04_crouch":     ("蓄力下蹲", "crouching down low with lowered center of gravity, preparing to spring up"),
    "05_paw_lift":   ("抬爪预备", "standing on hind feet, lifting front paw/wing halfway, preparing to wave"),
    "06_wave_ready": ("挥手·举爪", "right front paw/wing raised upright beside its head, open paw ready to wave"),
    "07_wave_left":  ("挥手·挥左", "right front paw/wing waving tilted towards the left"),
    "08_wave_right": ("挥手·挥右", "right front paw/wing waving tilted towards the right"),
    "09_bounce":     ("开心蹦跳", "springing upward happily with feet just leaving the ground"),
    "10_jump_apex":  ("腾空大跳", "leaping high at apex of jump in mid-air, joyful triumphant celebration"),
    "11_landing":    ("落地缓冲", "landing softly on the ground, bending knees to absorb the landing comfortably"),
    "12_nod":        ("满意点头", "nodding head down once politely and happily with an earnest approving smile"),
    "13_shuffle":    ("原地小碎步", "cute rapid shuffle steps in place, shifting weight playfully from foot to foot"),
    "14_lookback":   ("转身回望", "turning body into three-quarter rear angle, glancing back cutely over shoulder"),
    "15_settle":     ("收势站定", "turning back to front, clean composed standing posture"),
    "16_wink_smile": ("眨眼微笑收官", "confident front facing pose, winking one eye cheerfully with a bright finishing smile"),
}


def main():
    ap = argparse.ArgumentParser(description="角色 16 动作生成（卡→动作，ComfyUI EDIT）")
    ap.add_argument("--species", required=True)
    ap.add_argument("--stage", required=True, type=int, choices=[1, 2, 3])
    ap.add_argument("--card", required=True, help="定稿角色卡 PNG（动作形象锚）")
    ap.add_argument("--actions", default=",".join(DEFAULT_ACTIONS),
                    help="逗号分隔动作 id，默认全部 16 个")
    ap.add_argument("--seeds", default=",".join(map(str, DEFAULT_SEEDS)))
    ap.add_argument("--out-dir", default="design/actions_raw")
    args = ap.parse_args()

    key = (args.species, args.stage)
    if key not in APPEARANCE:
        print("错误：无该阶段外观模板（gen_wave_frames.APPEARANCE）", key,
              file=sys.stderr)
        return 2
    unknown = [a for a in args.actions.split(",") if a not in DEFAULT_ACTIONS]
    if unknown:
        print("错误：未知动作 id", unknown,
              "可选：", ",".join(DEFAULT_ACTIONS), file=sys.stderr)
        return 2
    if not os.path.exists(args.card):
        print("错误：缺角色卡", args.card, file=sys.stderr)
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

    seeds = [int(s) for s in args.seeds.split(",")]
    for act in args.actions.split(","):
        zh, pose = DEFAULT_ACTIONS[act]
        prompt = APPEARANCE[key] + ", " + pose
        for seed in seeds:
            tag = f"{act}_s{seed}"
            if tag in meta:
                print("skip existing", tag, flush=True)
                continue
            outs = queue_and_wait(build_graph(ref_name, prompt, NEGATIVE, seed))
            fetch_png(outs, os.path.join(out_dir, tag + ".png"))
            meta[tag] = {"action": act, "zh": zh, "seed": seed, "prompt": prompt,
                         "negative": NEGATIVE, "mode_gen": "EDIT denoise=1.0",
                         "latent": "640x1024", "steps": 25, "cfg": 1,
                         "sampler": "euler/simple", "ref_card": args.card}
            with open(meta_path, "w", encoding="utf-8") as fp:
                json.dump(meta, fp, ensure_ascii=False, indent=1)
            print("done", tag, zh, flush=True)
    print("ALL-DONE", key, args.actions)
    return 0


if __name__ == "__main__":
    sys.exit(main())
