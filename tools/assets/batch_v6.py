#!/usr/bin/env python3
"""#37 v6 批量：6 只 × 2 关键位帧 × 2 seed inpaint（原图保底，仅肢体包络重绘）。

每只产出：{out}/{pet}_pose{1,2}_s{seed}.png（已 Composite 回贴，遮罩外=原图像素）。
姿态语义：bunny=右耳内摆/外摆；chick=右翅上举挥左/挥右。
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from inpaint_client import build_graph, run, fetch, upload

BASE = r"C:/Users/hxx/AppData/Local/Temp/m9v6"
OUT = os.path.join(BASE, "out")

# 每只：外观锚定模板 + 两个关键位姿态短语（除姿态外逐字一致）
SPEC = {
    "bunny_1": dict(
        appear=("The exact same cream-colored plush robot bunny from the reference image: "
                "oversized round head, chibi toy proportions, short dense velvety plush fur, "
                "two tall upright ears with glowing pink heart-shaped LED lights inside, HUGE "
                "round glossy black eyes with big white sparkle highlights, tiny pink triangle "
                "nose, pink blush cheeks, wearing a beige collar with a golden round tag, "
                "soft studio lighting, plain pure white background, full body, centered composition"),
        pose1="its right-side ear bent and tilted toward the left toward the middle of its head like waving, everything else stays exactly the same, plain pure white background",
        pose2="its right-side ear leaning outward to the right away from its head like waving, everything else stays exactly the same, plain pure white background"),
    "bunny_2": dict(
        appear=("The exact same armored cream plush robot bunny from the reference image: "
                "oversized round head, short dense cream plush fur, two tall upright ears with "
                "glowing pink heart-shaped LED lights inside, round dark eyes, beige tactical "
                "collar with a small pendant, cream-colored mechanical shoulder armor and "
                "robotic gauntlet arms, soft studio lighting, plain pure white background, "
                "full body, centered composition"),
        pose1="its right-side ear bent and tilted toward the left toward the middle of its head like waving, everything else stays exactly the same, plain pure white background",
        pose2="its right-side ear leaning outward to the right away from its head like waving, everything else stays exactly the same, plain pure white background"),
    "bunny_3": dict(
        appear=("The exact same cream fluffy plush bunny from the reference image: "
                "very round fluffy body, two very tall upright plush ears with glowing pink "
                "heart-shaped LED lights inside, big round dark eyes, pink blush cheeks, tiny "
                "pink nose, white wired headphones resting around its neck, soft studio "
                "lighting, plain pure white background, full body, centered composition"),
        pose1="its right-side ear bent and tilted toward the left toward the middle of its head like waving, everything else stays exactly the same, plain pure white background",
        pose2="its right-side ear leaning outward to the right away from its head like waving, everything else stays exactly the same, plain pure white background"),
    "chick_1": dict(
        appear=("The exact same round yellow robot chick from the reference image: "
                "a cracked white eggshell half worn as a helmet on its head, big round glossy "
                "black eyes, small orange beak, chubby yellow rubbery body with subtle panel "
                "lines, tiny yellow wings, orange feet, soft studio lighting, plain pure white "
                "background, full body, centered composition"),
        pose1="its right-side wing raised up beside its head, wing tip tilted to the left like waving hello, everything else stays exactly the same, plain pure white background",
        pose2="its right-side wing raised up beside its head, wing tip tilted to the right like waving hello, everything else stays exactly the same, plain pure white background"),
    "chick_2": dict(
        appear=("The exact same yellow robot chick from the reference image: "
                "cracked white eggshell helmet on its head, black visor face with two glowing "
                "round yellow eyes, small orange beak, yellow mechanical body with panel lines "
                "and tiny lights, small mechanical wings, orange robot feet, soft studio "
                "lighting, plain pure white background, full body, centered composition"),
        pose1="its right-side mechanical wing raised up beside its head tilted to the left like waving hello, everything else stays exactly the same, plain pure white background",
        pose2="its right-side mechanical wing raised up beside its head tilted to the right like waving hello, everything else stays exactly the same, plain pure white background"),
    "chick_3": dict(
        appear=("The exact same stocky yellow armored robot chick from the reference image: "
                "white and yellow space helmet on its head, big round dark eyes, orange beak, "
                "yellow armor plates with small glowing round indicators, large round shoulder "
                "shields with a star emblem, orange armored feet, soft studio lighting, plain "
                "pure white background, full body, centered composition"),
        pose1="its left-side arm raised up beside its helmet tilted to the left like waving hello, everything else stays exactly the same, plain pure white background",
        pose2="its left-side arm raised up beside its helmet tilted to the right like waving hello, everything else stays exactly the same, plain pure white background"),
}

SEEDS = [68, 101]


def main():
    meta = {}
    for pet, spec in SPEC.items():
        orig = os.path.join(BASE, f"{pet}_white.png")
        mask = os.path.join(BASE, f"{pet}_mask.png")
        if not os.path.exists(orig) or not os.path.exists(mask):
            print("缺文件", pet, file=sys.stderr)
            return 2
        o = upload(orig)
        for pose_key in ("pose1", "pose2"):
            prompt = spec["appear"] + ", " + spec[pose_key]
            for seed in SEEDS:
                tag = f"{pet}_{pose_key}_s{seed}"
                outp = os.path.join(OUT, tag + ".png")
                if os.path.exists(outp):
                    print("skip", tag, flush=True)
                    continue
                outs = run(build_graph(o["name"], os.path.basename(mask), prompt, seed))
                for p in fetch(outs, OUT):
                    if "v6_comp" in p:
                        os.replace(p, outp)
                meta[tag] = {"pet": pet, "pose": pose_key, "seed": seed,
                             "prompt": prompt, "mask": os.path.basename(mask),
                             "mode": "inpaint denoise=1.0 ImageCompositeMasked",
                             "steps": 25, "cfg": 1, "sampler": "euler/simple"}
                print("done", tag, flush=True)
        with open(os.path.join(OUT, "meta_v6.json"), "w", encoding="utf-8") as f:
            json.dump(meta, f, ensure_ascii=False, indent=1)
    print("ALL-DONE")


if __name__ == "__main__":
    sys.exit(main())
