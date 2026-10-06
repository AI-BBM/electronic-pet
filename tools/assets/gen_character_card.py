#!/usr/bin/env python3
"""角色卡生成（管线第 1 步）：线上定稿静态图 → 高清角色设定卡。

工艺与 M9 v5 同源（探针定案）：参考图 latent 进 TextEncodeQwenImage21 +
EmptyLatent + KSampler denoise=1.0；固定 seed 列保形象。卡片是后续 16 动作
的唯一形象锚（gen_actions_16 以卡为参考图），故先出卡、后出动作、再超分。

两种卡型：
  front  定稿正视图卡——单只全身正面站姿白底（默认，动作生成锚定用）；
  sheet  三视图设定卡——正/侧/背三视图并排（设定集/评审用，负面词放开
         multiple characters）。

用法（本机 ComfyUI 8188）：
  python gen_character_card.py --species bunny --stage 3 \
      --src design/assets_output/pets/bunny/3.png [--mode front,sheet]
产出：{out}/{species}_{stage}/card_{mode}_s{seed}.png + meta.json。
"""
import argparse
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_wave_frames import APPEARANCE  # 各阶段外观锚定模板（M9 手调定稿）
import upscale_sr

HOST = upscale_sr.HOST

NEGATIVE = upscale_sr.__dict__.get("NEGATIVE") or (
    "background scene, gradient background, colored background, floor shadow, "
    "multiple characters, deformed, extra limbs, blurry, text, watermark")
# 三视图必须放开 multiple characters（负面词与卡型互斥）
NEG_SHEET = ("background scene, gradient background, colored background, floor shadow, "
             "deformed, extra limbs, blurry, text, watermark")

CARD_FRONT = ("official character design card, single character only, full body "
              "front view, standing upright in a neutral relaxed pose, facing the "
              "camera, symmetrical, crisp clean pure white background, high detail, "
              "centered composition")
CARD_SHEET = ("character model sheet, the exact same character shown three times "
              "side by side at equal size: front view, side view, back view, all "
              "standing on one shared ground line, crisp clean pure white background, "
              "no text, no labels, centered composition")

DEFAULT_SEEDS = [68, 101]

# 外观基准 V2：以 OSS 静态图逐特征核对的人工描述（PM 打回 v1 卡后重写——
# gen_wave_frames.APPEARANCE 的 bunny_3 缺肩甲/项圈描述，不能作为身份规范）。
# 新物种/新阶段出卡前必须先看静态图补本表。
APPEARANCE_V2 = {
    ("bunny", 1): (
        "The exact same small cream ivory plush baby bunny from the reference "
        "image: very round oversized head on a tiny chubby sitting body, front "
        "paws resting together, short dense cream plush fur, two tall upright "
        "ears with pink LED screen heart lights glowing inside, HUGE round glossy "
        "dark eyes with big sparkle highlights, pink blush cheeks, beige studded "
        "collar with a round golden tag, no armor, baby chibi proportions, soft "
        "studio lighting, plain pure white background, full body, centered "
        "composition"),
    ("bunny", 2): (
        "The exact same cream-beige plush bunny from the reference image: standing "
        "upright on hind legs, chunky round body with short dense beige plush fur, "
        "two tall upright ears with warm golden screen heart lights glowing inside, "
        "big round dark eyes, tiny pink nose, beige studded collar with a round "
        "golden tag, no armor, plump chibi toy proportions, soft studio lighting, "
        "plain pure white background, full body, centered composition"),
    ("bunny", 3): (
        "The exact same cream-beige plush bunny from the reference image: chunky "
        "round body with short dense beige-cream plush fur, two tall upright plush "
        "ears with glowing warm orange heart-shaped lights inside, big round dark "
        "brown eyes, tiny pink nose, gold mechanical armor plates covering both "
        "shoulders and forearms, metallic collar with a golden heart-shaped tag, "
        "white headphones resting behind its arms around the neck, stocky chibi "
        "toy proportions, soft studio lighting, plain pure white background, full "
        "body, centered composition"),
}


def build_graph(ref_name, prompt, negative, seed, width=640, height=1024,
                denoise=1.0, source_latent=False):
    """EDIT 定案图结构：参考 latent + 空 latent 全降噪（见 gen_wave_frames 探针）。

    source_latent=True 走低重绘 img2img（latent=VAEEncode(参考图)），形象
    物理锁死，用于与静态图同族的 A 型锚卡；denoise 需 <1。"""
    graph = {
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
        "6": {"class_type": "LoadImage", "inputs": {"image": ref_name}},
        "8": {"class_type": "TextEncodeQwenImage21",
              "inputs": {"clip": ["4", 0], "images": [["6", 0]], "vae": ["5", 0],
                         "prompt": prompt, "negative_prompt": negative,
                         "resolution": 1024}},
        "9": {"class_type": "EmptyLatentImage",
              "inputs": {"width": width, "height": height, "batch_size": 1}},
        "11": {"class_type": "KSampler",
               "inputs": {"model": ["3", 0], "positive": ["8", 0],
                          "negative": ["8", 1],
                          "latent_image": ["14", 0] if source_latent else ["9", 0],
                          "seed": seed, "steps": 25, "cfg": 1,
                          "sampler_name": "euler", "scheduler": "simple",
                          "denoise": denoise}},
        "12": {"class_type": "VAEDecode",
               "inputs": {"samples": ["11", 0], "vae": ["5", 0]}},
        "13": {"class_type": "SaveImage",
               "inputs": {"images": ["12", 0], "filename_prefix": "card"}},
    }
    if source_latent:
        graph["14"] = {"class_type": "VAEEncode",
                       "inputs": {"pixels": ["6", 0], "vae": ["5", 0]}}
    return graph


def queue_and_wait(graph, timeout=2400):
    pid = upscale_sr.api("/prompt", {"prompt": graph, "client_id": "card"})["prompt_id"]
    t0 = time.time()
    while time.time() - t0 < timeout:
        h = upscale_sr.api(f"/history/{pid}")
        if pid in h:
            status = h[pid].get("status", {})
            if status.get("status_str") == "error":
                raise RuntimeError(json.dumps(status, ensure_ascii=False)[:800])
            if status.get("completed") or h[pid].get("outputs"):
                return h[pid]["outputs"]
        time.sleep(4)
    raise TimeoutError(pid)


def fetch_png(outs, dst):
    import urllib.parse
    import urllib.request
    for o in outs.values():
        for img in o.get("images", []):
            q = urllib.parse.urlencode({
                "filename": img["filename"], "subfolder": img.get("subfolder", ""),
                "type": img.get("type", "output")})
            with urllib.request.urlopen(f"{HOST}/view?{q}", timeout=120) as r:
                data = r.read()
            with open(dst, "wb") as f:
                f.write(data)
            return dst
    raise RuntimeError("无输出图像 " + dst)


def main():
    ap = argparse.ArgumentParser(description="角色卡生成（ComfyUI EDIT 定稿卡）")
    ap.add_argument("--species", required=True)
    ap.add_argument("--stage", required=True, type=int, choices=[1, 2, 3])
    ap.add_argument("--src", required=True, help="线上定稿静态图（透明或白底 PNG）")
    ap.add_argument("--mode", default="front", choices=["front", "sheet"])
    ap.add_argument("--route", default="edit", choices=["edit", "img2img"],
                    help="edit=空latent全降噪(姿态可变)；img2img=源latent低重绘"
                         "(形象物理锁死，A型锚卡推荐)")
    ap.add_argument("--denoise", type=float, default=None,
                    help="默认 img2img=0.45 / edit=1.0")
    ap.add_argument("--seeds", default=",".join(map(str, DEFAULT_SEEDS)))
    ap.add_argument("--out-dir", default="design/character_cards")
    args = ap.parse_args()

    key = (args.species, args.stage)
    if key not in APPEARANCE:
        print("错误：无该阶段外观模板（gen_wave_frames.APPEARANCE）", key,
              file=sys.stderr)
        return 2
    if not os.path.exists(args.src):
        print("错误：缺源图", args.src, file=sys.stderr)
        return 2
    seeds = [int(s) for s in args.seeds.split(",")]

    # 参考图预处理：垫白 + RealESRGAN x2（源为 512 长边，直接喂 EDIT 会糊）
    os.makedirs(args.out_dir, exist_ok=True)
    flat = os.path.join(args.out_dir, f"{args.species}_{args.stage}_src_white.png")
    upscale_sr.flatten_white(args.src, flat)
    sr_dir = os.path.join(args.out_dir, f"{args.species}_{args.stage}_src_sr")
    sr_list = upscale_sr.run_one(flat, sr_dir, 2)
    ref_path = sr_list[0]
    ref_name = upscale_sr.upload_image(ref_path)["name"]
    print("ref:", ref_name, flush=True)

    out_dir = os.path.join(args.out_dir, f"{args.species}_{args.stage}")
    os.makedirs(out_dir, exist_ok=True)
    meta_path = os.path.join(out_dir, "meta.json")
    meta = {}
    if os.path.exists(meta_path):
        with open(meta_path, encoding="utf-8") as f:
            meta = json.load(f)

    negative = NEG_SHEET if args.mode == "sheet" else NEGATIVE
    appearance = APPEARANCE_V2.get(key)
    if appearance is None:
        print("警告：APPEARANCE_V2 无该阶段基准描述，回退 gen_wave_frames.APPEARANCE"
              "——出卡前必须先与 OSS 静态图逐特征核对！", key, file=sys.stderr)
        appearance = APPEARANCE[key]
    prompt = appearance + ", " + (CARD_SHEET if args.mode == "sheet" else CARD_FRONT)
    width, height = (1024, 640) if args.mode == "sheet" else (640, 1024)
    i2i = args.route == "img2img"
    denoise = args.denoise if args.denoise is not None else (0.45 if i2i else 1.0)
    if i2i and denoise >= 1.0:
        print("错误：img2img 路线 denoise 必须 <1（否则等价全重绘）", file=sys.stderr)
        return 2
    for seed in seeds:
        tag = f"card_{args.mode}_{args.route}_s{seed}"
        if tag in meta:
            print("skip existing", tag, flush=True)
            continue
        outs = queue_and_wait(build_graph(ref_name, prompt, negative, seed,
                                          width, height, denoise, i2i))
        fetch_png(outs, os.path.join(out_dir, tag + ".png"))
        meta[tag] = {"mode": args.mode, "route": args.route, "denoise": denoise,
                     "seed": seed, "prompt": prompt,
                     "negative": negative, "mode_gen": f"{args.route} d={denoise}",
                     "latent": f"{width}x{height}", "steps": 25, "cfg": 1,
                     "sampler": "euler/simple", "ref": ref_name,
                     "src": args.src}
        with open(meta_path, "w", encoding="utf-8") as fp:
            json.dump(meta, fp, ensure_ascii=False, indent=1)
        print("done", tag, flush=True)
    print("ALL-DONE", key, args.mode)
    return 0


if __name__ == "__main__":
    sys.exit(main())
