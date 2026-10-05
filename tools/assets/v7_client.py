#!/usr/bin/env python3
"""#37 v7 三步工艺（黄总 2026-10-05 指定）：
① 多角度角色图：原静态图 → 左侧/右侧/背面视图（EDIT，角色资产锁定）；
② 16 动作图：以 正面+左侧+右侧 三视图作多参考，逐帧显式挥手姿态短语
   （16 帧姿态序列，固定 seed）；
③ 裁剪+超分：主体裁剪 → RealESRGAN x2 → 回落 512 高（细节增强）。
"""
import json
import os
import sys
import time
import urllib.parse
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from inpaint_client import api, upload, fetch, NEG

HOST = "http://127.0.0.1:8188"

# 每只外观锚定模板（同 v6）
APPEAR = {
    "bunny_1": ("The exact same cream-colored plush robot bunny from the reference image: "
                "oversized round head, chibi toy proportions, short dense velvety plush fur, "
                "two tall upright ears with glowing pink heart-shaped LED lights inside, HUGE "
                "round glossy black eyes with big white sparkle highlights, tiny pink triangle "
                "nose, pink blush cheeks, wearing a beige collar with a golden round tag, "
                "soft studio lighting, plain pure white background, full body, centered composition"),
    "bunny_2": ("The exact same armored cream plush robot bunny from the reference image: "
                "oversized round head, short dense cream plush fur, two tall upright ears with "
                "glowing pink heart-shaped LED lights inside, round dark eyes, beige tactical "
                "collar with a small pendant, cream-colored mechanical shoulder armor and "
                "robotic gauntlet arms, soft studio lighting, plain pure white background, "
                "full body, centered composition"),
    "bunny_3": ("The exact same cream fluffy plush bunny from the reference image: "
                "very round fluffy body, two very tall upright plush ears with glowing pink "
                "heart-shaped LED lights inside, big round dark eyes, pink blush cheeks, tiny "
                "pink nose, white wired headphones resting around its neck, soft studio "
                "lighting, plain pure white background, full body, centered composition"),
    "chick_1": ("The exact same round yellow robot chick from the reference image: "
                "a cracked white eggshell half worn as a helmet on its head, big round glossy "
                "black eyes, small orange beak, chubby yellow rubbery body with subtle panel "
                "lines, tiny yellow wings, orange feet, soft studio lighting, plain pure white "
                "background, full body, centered composition"),
    "chick_2": ("The exact same yellow robot chick from the reference image: "
                "cracked white eggshell helmet on its head, black visor face with two glowing "
                "round yellow eyes, small orange beak, yellow mechanical body with panel lines "
                "and tiny lights, small mechanical wings, orange robot feet, soft studio "
                "lighting, plain pure white background, full body, centered composition"),
    "chick_3": ("The exact same stocky yellow armored robot chick from the reference image: "
                "white and yellow space helmet on its head, big round dark eyes, orange beak, "
                "yellow armor plates with small glowing round indicators, large round shoulder "
                "shields with a star emblem, orange armored feet, soft studio lighting, plain "
                "pure white background, full body, centered composition"),
}

ANGLES = {
    "left": "seen from its left side, full body in profile facing left",
    "right": "seen from its right side, full body in profile facing right",
    "back": "seen from behind, full body back view",
}

# 16 帧挥手姿态序列（bunny=右前爪；chick=右翅/左臂）
POSES16 = {
    "bunny": [
        "sitting still naturally with front paws resting on the ground, looking straight at the camera",
        "sitting, beginning to lift its right front paw slightly off the ground",
        "sitting, right front paw lifted to chest height",
        "sitting, right front paw raised beside its head",
        "sitting, right front paw raised beside its head, open paw tilted to the left",
        "sitting, right front paw raised beside its head, open paw tilted to the right",
        "sitting, right front paw raised beside its head, open paw tilted to the left",
        "sitting, right front paw raised beside its head, open paw tilted to the right",
        "sitting, right front paw raised beside its head, open paw tilted to the left",
        "sitting, right front paw raised beside its head, open paw tilted to the right",
        "sitting, right front paw raised beside its head, open paw tilted to the left",
        "sitting, right front paw raised beside its head, open paw tilted to the right",
        "sitting, right front paw starting to lower from beside its head",
        "sitting, right front paw lowered to chest height",
        "sitting, right front paw resting back on the ground",
        "sitting still naturally with front paws resting on the ground, looking straight at the camera",
    ],
    "chick": [
        "standing still naturally with wings resting at its sides, looking straight at the camera",
        "standing, beginning to lift its right wing slightly",
        "standing, right wing lifted to shoulder height",
        "standing, right wing raised up beside its head",
        "standing, right wing raised up beside its head, wing tip tilted to the left",
        "standing, right wing raised up beside its head, wing tip tilted to the right",
        "standing, right wing raised up beside its head, wing tip tilted to the left",
        "standing, right wing raised up beside its head, wing tip tilted to the right",
        "standing, right wing raised up beside its head, wing tip tilted to the left",
        "standing, right wing raised up beside its head, wing tip tilted to the right",
        "standing, right wing raised up beside its head, wing tip tilted to the left",
        "standing, right wing raised up beside its head, wing tip tilted to the right",
        "standing, right wing starting to lower from beside its head",
        "standing, right wing lowered to shoulder height",
        "standing, right wing resting back at its side",
        "standing still naturally with wings resting at its sides, looking straight at the camera",
    ],
}


def build_multiref_graph(ref_names, prompt, seed, width=640, height=1024):
    """多参考图（角色设定表）+ EDIT d1.0。ref_names: [正面, 左侧, 右侧]。"""
    g = {
        "2": {"class_type": "UNETLoader",
              "inputs": {"unet_name": "qwen_image_2.1_int8_convrot.safetensors",
                         "weight_dtype": "default"}},
        "3": {"class_type": "QwenImage21Cache",
              "inputs": {"model": ["2", 0], "device": "auto", "dtype": "default"}},
        "4": {"class_type": "CLIPLoader",
              "inputs": {"clip_name": "qwen3vl_8b_w4a8.safetensors",
                         "type": "qwen_image", "device": "default"}},
        "5": {"class_type": "VAELoader",
              "inputs": {"vae_name": "qwen_image_2.1_vae_bf16.safetensors"}},
        "16": {"class_type": "TextEncodeQwenImage21",
               "inputs": {"clip": ["4", 0],
                          "images": [[n, 0] for n in ref_names],
                          "vae": ["5", 0], "prompt": prompt,
                          "negative_prompt": NEG, "resolution": 768}},
        "9": {"class_type": "EmptyLatentImage",
              "inputs": {"width": width, "height": height, "batch_size": 1}},
        "11": {"class_type": "KSampler",
               "inputs": {"model": ["3", 0], "positive": ["16", 0],
                          "negative": ["16", 1], "latent_image": ["9", 0],
                          "seed": seed, "steps": 25, "cfg": 1,
                          "sampler_name": "euler", "scheduler": "simple",
                          "denoise": 1.0}},
        "12": {"class_type": "VAEDecode",
               "inputs": {"samples": ["11", 0], "vae": ["5", 0]}},
        "13": {"class_type": "SaveImage",
               "inputs": {"images": ["12", 0], "filename_prefix": "v7"}},
    }
    return g


def run_graph(graph, timeout=1200):
    pid = api("/prompt", {"prompt": graph, "client_id": "v7"})["prompt_id"]
    t0 = time.time()
    while time.time() - t0 < timeout:
        h = api(f"/history/{pid}")
        if pid in h:
            st = h[pid].get("status", {})
            if st.get("status_str") == "error":
                raise RuntimeError(json.dumps(st, ensure_ascii=False)[:800])
            if st.get("completed") or h[pid].get("outputs"):
                return h[pid]["outputs"]
        time.sleep(4)
    raise TimeoutError(pid)


def gen_one(ref_names, prompt, seed, out_path):
    outs = run_graph(build_multiref_graph(ref_names, prompt, seed))
    for o in outs.values():
        for img in o.get("images", []):
            q = urllib.parse.urlencode({
                "filename": img["filename"], "subfolder": img.get("subfolder", ""),
                "type": img.get("type", "output")})
            with urllib.request.urlopen(f"{HOST}/view?{q}", timeout=60) as r:
                data = r.read()
            with open(out_path, "wb") as f:
                f.write(data)
    return out_path
