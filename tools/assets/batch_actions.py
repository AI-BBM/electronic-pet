#!/usr/bin/env python3
"""v7 ②：16 动作帧批量——三视图多参考（正面+左侧+右侧）+ 逐帧显式姿态 + 固定 seed。

产出 out/actions/{pet}_f{00..15}_s{seed}.png + meta_actions.json。
用法：python batch_actions.py --pet bunny_1 [--seeds 68,101]
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import v7_client as v

BASE = r"C:/Users/hxx/AppData/Local/Temp/m9v7"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pet", required=True)
    ap.add_argument("--seeds", default="68")
    ap.add_argument("--frames", default=",".join(str(i) for i in range(16)))
    args = ap.parse_args()
    pet = args.pet
    seeds = [int(s) for s in args.seeds.split(",")]
    frames = [int(f) for f in args.frames.split(",")]

    adir = os.path.join(BASE, "out", "angles")
    refs = [os.path.join(BASE, f"{pet}_white.png"),
            os.path.join(adir, f"{pet}_left_s68.png"),
            os.path.join(adir, f"{pet}_right_s68.png")]
    for p in refs:
        if not os.path.exists(p):
            print("缺参考图", p, file=sys.stderr)
            return 2
    ref_names = [v.upload(p)["name"] for p in refs]
    print("refs uploaded:", ref_names, flush=True)

    out_dir = os.path.join(BASE, "out", "actions")
    os.makedirs(out_dir, exist_ok=True)
    meta_path = os.path.join(out_dir, "meta_actions.json")
    meta = {}
    if os.path.exists(meta_path):
        meta = json.load(open(meta_path, encoding="utf-8"))

    species = pet.rsplit("_", 1)[0]
    for f in frames:
        prompt = v.APPEAR[pet] + ", " + v.POSES16[species][f]
        for seed in seeds:
            tag = f"{pet}_f{f:02d}_s{seed}"
            out_path = os.path.join(out_dir, tag + ".png")
            if os.path.exists(out_path):
                print("skip", tag, flush=True)
                continue
            v.gen_one(ref_names, prompt, seed, out_path)
            meta[tag] = {"pet": pet, "frame": f, "seed": seed, "prompt": prompt,
                         "refs": ref_names, "mode": "multiref EDIT d1.0",
                         "resolution": 768, "steps": 25, "cfg": 1,
                         "sampler": "euler/simple"}
            with open(meta_path, "w", encoding="utf-8") as fp:
                json.dump(meta, fp, ensure_ascii=False, indent=1)
            print("done", tag, flush=True)
    print("ACTIONS-DONE", pet)


if __name__ == "__main__":
    sys.exit(main())
