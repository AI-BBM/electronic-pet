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

# 16 动作清单草案 v1（id → 中文名 + 显式动作短语；短语逐动作独立、其余上下文一致）
DEFAULT_ACTIONS = {
    "idle":  ("待机", "standing naturally and relaxed, front paws resting, looking "
                     "straight at the camera with a gentle smile"),
    "wave":  ("挥手", "right front paw raised beside its head, open paw waving hello"),
    "jump":  ("跳跃", "leaping joyfully in mid-air, all feet off the ground, ears and "
                     "fur bouncing upward, delighted expression"),
    "cheer": ("欢呼", "both front paws raised high above its head in celebration, "
                     "eyes sparkling with star highlights, cheering"),
    "walk":  ("行走", "mid-step walking forward, one front paw lifted, gentle head bob"),
    "run":   ("奔跑", "running fast, body leaning forward, cheeks pressed by the wind, "
                     "determined happy face"),
    "sit":   ("端坐", "sitting down upright with back straight, front paws together, "
                     "attentive expression"),
    "sleep": ("睡觉", "curled up sleeping peacefully on the ground, eyes closed, tiny "
                     "breathing motion, a small floating z outline above its head"),
    "eat":   ("进食", "holding a small treat in its front paws, nibbling and chewing "
                     "happily with puffed cheeks"),
    "dance": ("跳舞", "dancing happily, hips swaying side to side, one front paw "
                     "pointing up to the sky"),
    "nod":   ("点头", "nodding its head down once politely in agreement, earnest eyes"),
    "shake": ("摇头", "shaking its head side to side saying no, cheeks wobbling"),
    "sad":   ("委屈", "drooping and dejected, head and ears hanging down, big glossy "
                     "teary eyes looking up, quivering lower lip"),
    "clap":  ("鼓掌", "clapping its front paws together happily in applause"),
    "bow":   ("鞠躬", "bowing forward politely at the waist as a formal thank-you "
                     "greeting"),
    "spin":  ("转圈", "spinning around joyfully on one foot, arms spread out, dizzy "
                     "happy smile"),
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
