#!/usr/bin/env python3
"""v7 ①：6 只 × 3 角度（左/右/背）角色图批量，产出 out/angles/{pet}_{angle}_s68.png。"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import v7_client as v

BASE = r"C:/Users/hxx/AppData/Local/Temp/m9v7"
PETS = ["bunny_1", "bunny_2", "bunny_3", "chick_1", "chick_2", "chick_3"]


def main():
    os.makedirs(os.path.join(BASE, "out", "angles"), exist_ok=True)
    meta = {}
    meta_path = os.path.join(BASE, "out", "angles", "meta_angles.json")
    if os.path.exists(meta_path):
        meta = json.load(open(meta_path, encoding="utf-8"))
    for pet in PETS:
        o = v.upload(os.path.join(BASE, f"{pet}_white.png"))
        for angle, phrase in v.ANGLES.items():
            tag = f"{pet}_{angle}_s68"
            out_path = os.path.join(BASE, "out", "angles", tag + ".png")
            if os.path.exists(out_path):
                print("skip", tag, flush=True)
                continue
            prompt = v.APPEAR[pet] + ", " + phrase
            v.gen_one([o["name"]], prompt, 68, out_path)
            meta[tag] = {"pet": pet, "angle": angle, "seed": 68,
                         "prompt": prompt, "ref": o["name"], "mode": "EDIT d1.0"}
            with open(meta_path, "w", encoding="utf-8") as f:
                json.dump(meta, f, ensure_ascii=False, indent=1)
            print("done", tag, flush=True)
    print("ANGLES-DONE")


if __name__ == "__main__":
    sys.exit(main())
