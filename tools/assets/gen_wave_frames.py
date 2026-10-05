#!/usr/bin/env python3
"""#37 M9 v5 挥手帧生成客户端：驱动本机 ComfyUI（Qwen-Image 2.1 EDIT 管线）。

v5 工艺（PM 2026-10-05 定稿，覆盖 v1-v4 路线）：
1. 形象锁定：OSS 静态图（白底版）作参考图进 TextEncodeQwenImage21（参考图以
   VAE latent 拼接进 text encoder 序列），固定 seed 组；提示词模板除姿态短语外
   逐帧完全一致；
2. 姿态序列显式逐帧写出（自然站姿 → 抬爪 → 挥左 → 挥右 → … → 归位 → 站姿），
   禁止整图笼统重述；
3. 每帧生成多候选（默认 seed 68/101/202），人工挑帧间形象/色调最一致的一组；
   个别帧不一致单帧重摇（--frames 重跑即续跑，已有产物自动跳过）。

探针结论（2026-10-05，bunny_1 实测）：
- EDIT latent + denoise=1.0：姿态变化 ✓、白底保持 ✓、同 seed 帧间一致 ✓（采用）；
- 该 latent 走 KSampler 低重绘（denoise<1）输出纯噪声糊——不可用；
- 标准 VAEEncode img2img：形象锁 ✓ 但 denoise≤0.85 抬不出爪（结构变化被
  参考 latent 锁死）——不可用。

用法（需本机 ComfyUI 已启动，8188 端口；参考白底图置于 --ref-dir）：
  python gen_wave_frames.py --species bunny --stage 1
      [--frames 0,1,2,3,4,5,6,7] [--seeds 68,101,202]
产出：{out}/{species}_{stage}/f{帧}_s{seed}.png + meta.json（逐帧 prompt/seed）。
"""
import argparse
import json
import os
import sys
import time
import urllib.parse
import urllib.request

HOST = "http://127.0.0.1:8188"

NEGATIVE = ("background scene, gradient background, colored background, floor shadow, "
            "multiple characters, deformed, extra limbs, blurry, text, watermark")

SEEDS = [68, 101, 202]

# 每阶段外观锚定模板（除姿态短语外逐帧完全一致——工艺①）
APPEARANCE = {
    ("bunny", 1): (
        "The exact same cream-colored plush robot bunny from the reference image: "
        "oversized round head, chibi toy proportions, short dense velvety plush fur, "
        "two tall upright ears with glowing pink heart-shaped LED lights inside, HUGE "
        "round glossy black eyes with big white sparkle highlights, tiny pink triangle "
        "nose, pink blush cheeks, wearing a beige collar with a golden round tag, "
        "soft studio lighting, plain pure white background, full body, centered composition"),
    ("bunny", 2): (
        "The exact same armored cream plush robot bunny from the reference image: "
        "oversized round head, short dense cream plush fur, two tall upright ears with "
        "glowing pink heart-shaped LED lights inside, round dark eyes, beige tactical "
        "collar with a small pendant, cream-colored mechanical shoulder armor and "
        "robotic gauntlet arms, soft studio lighting, plain pure white background, "
        "full body, centered composition"),
    ("bunny", 3): (
        "The exact same cream fluffy plush bunny from the reference image: very round "
        "fluffy body, two very tall upright plush ears with glowing pink heart-shaped "
        "LED lights inside, big round dark eyes, pink blush cheeks, tiny pink nose, "
        "white wired headphones resting around its neck, soft studio lighting, plain "
        "pure white background, full body, centered composition"),
    ("chick", 1): (
        "The exact same round yellow robot chick from the reference image: a cracked "
        "white eggshell half worn as a helmet on its head, big round glossy black eyes, "
        "small orange beak, chubby yellow rubbery body with subtle panel lines, tiny "
        "yellow wings, orange feet, soft studio lighting, plain pure white background, "
        "full body, centered composition"),
    ("chick", 2): (
        "The exact same yellow robot chick from the reference image: cracked white "
        "eggshell helmet on its head, black visor face with two glowing round yellow "
        "eyes, small orange beak, yellow mechanical body with panel lines and tiny "
        "lights, small mechanical wings, orange robot feet, soft studio lighting, "
        "plain pure white background, full body, centered composition"),
    ("chick", 3): (
        "The exact same stocky yellow armored robot chick from the reference image: "
        "white and yellow space helmet on its head, big round dark eyes, orange beak, "
        "yellow armor plates with small glowing round indicators, large round shoulder "
        "shields with a star emblem, orange armored feet, soft studio lighting, plain "
        "pure white background, full body, centered composition"),
}

# 姿态序列（工艺②：逐帧显式写出）
POSES = {
    "bunny": [
        "sitting still naturally with front paws resting on the ground, looking straight at the camera",
        "sitting on the ground, right front paw lifted halfway, beginning to wave hello",
        "sitting on the ground, right front paw raised beside its head, open paw waving tilted to the left",
        "sitting on the ground, right front paw raised beside its head, open paw waving tilted to the right",
        "sitting on the ground, right front paw raised beside its head, open paw waving tilted to the left",
        "sitting on the ground, right front paw raised beside its head, open paw waving tilted to the right",
        "sitting on the ground, right front paw lowered halfway after finishing the wave",
        "sitting still naturally with front paws resting on the ground, looking straight at the camera",
    ],
    "chick": [
        "standing still naturally with wings resting at its sides, looking straight at the camera",
        "standing on the ground, right wing lifted halfway, beginning to wave hello",
        "standing on the ground, right wing raised up beside its head, wing tip waving tilted to the left",
        "standing on the ground, right wing raised up beside its head, wing tip waving tilted to the right",
        "standing on the ground, right wing raised up beside its head, wing tip waving tilted to the left",
        "standing on the ground, right wing raised up beside its head, wing tip waving tilted to the right",
        "standing on the ground, right wing lowered halfway after finishing the wave",
        "standing still naturally with wings resting at its sides, looking straight at the camera",
    ],
}


def api(path, data=None):
    req = urllib.request.Request(
        HOST + path,
        data=None if data is None else json.dumps(data).encode(),
        headers={} if data is None else {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read().decode())


def upload_image(path):
    import mimetypes
    boundary = "----waveboundary"
    fname = os.path.basename(path)
    with open(path, "rb") as f:
        content = f.read()
    body = b"".join([
        f"--{boundary}\r\n".encode(),
        f'Content-Disposition: form-data; name="image"; filename="{fname}"\r\n'.encode(),
        f"Content-Type: {mimetypes.guess_type(fname)[0] or 'image/png'}\r\n\r\n".encode(),
        content, b"\r\n",
        f"--{boundary}\r\n".encode(),
        b'Content-Disposition: form-data; name="overwrite"\r\n\r\ntrue\r\n',
        f"--{boundary}\r\n".encode(),
        b'Content-Disposition: form-data; name="type"\r\n\r\ninput\r\n',
        f"--{boundary}--\r\n".encode(),
    ])
    req = urllib.request.Request(HOST + "/upload/image", data=body, method="POST",
                                 headers={"Content-Type":
                                          f"multipart/form-data; boundary={boundary}"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.loads(r.read().decode())


def build_graph(ref_name, prompt, seed):
    """Qwen-Image 2.1 EDIT：参考图 latent + KSampler 全降噪（探针定案参数）。"""
    return {
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
                         "prompt": prompt, "negative_prompt": NEGATIVE,
                         "resolution": 1024}},
        "9": {"class_type": "EmptyLatentImage",
              "inputs": {"width": 640, "height": 1024, "batch_size": 1}},
        "10": {"class_type": "ComfySwitchNode",
               "inputs": {"on_false": ["8", 2], "on_true": ["9", 0],
                          "switch": True}},
        "11": {"class_type": "KSampler",
               "inputs": {"model": ["3", 0], "positive": ["8", 0],
                          "negative": ["8", 1], "latent_image": ["10", 0],
                          "seed": seed, "steps": 25, "cfg": 1,
                          "sampler_name": "euler", "scheduler": "simple",
                          "denoise": 1.0}},
        "12": {"class_type": "VAEDecode",
               "inputs": {"samples": ["11", 0], "vae": ["5", 0]}},
        "13": {"class_type": "SaveImage",
               "inputs": {"images": ["12", 0], "filename_prefix": "wave"}},
    }


def queue_and_wait(graph, timeout=1200):
    pid = api("/prompt", {"prompt": graph, "client_id": "wave"})["prompt_id"]
    t0 = time.time()
    while time.time() - t0 < timeout:
        h = api(f"/history/{pid}")
        if pid in h:
            status = h[pid].get("status", {})
            if status.get("status_str") == "error":
                raise RuntimeError(json.dumps(status, ensure_ascii=False)[:800])
            if status.get("completed") or h[pid].get("outputs"):
                return h[pid]["outputs"]
        time.sleep(4)
    raise TimeoutError(pid)


def main():
    ap = argparse.ArgumentParser(description="#37 v5 挥手帧生成（ComfyUI EDIT）")
    ap.add_argument("--species", required=True, choices=["bunny", "chick"])
    ap.add_argument("--stage", required=True, type=int, choices=[1, 2, 3])
    ap.add_argument("--ref-dir", required=True, help="白底参考图目录")
    ap.add_argument("--out-dir", required=True, help="候选帧输出根目录")
    ap.add_argument("--frames", default="0,1,2,3,4,5,6,7", help="生成/重摇帧")
    ap.add_argument("--seeds", default=",".join(map(str, SEEDS)))
    args = ap.parse_args()

    key = (args.species, args.stage)
    if key not in APPEARANCE:
        print("错误：无该阶段外观模板", key, file=sys.stderr)
        return 2
    seeds = [int(s) for s in args.seeds.split(",")]
    frames = [int(f) for f in args.frames.split(",")]

    src = os.path.join(args.ref_dir, f"{args.species}_{args.stage}_white.png")
    if not os.path.exists(src):
        print("错误：缺参考白底图", src, file=sys.stderr)
        return 2
    ref_name = upload_image(src)["name"]
    print("ref uploaded:", ref_name, flush=True)

    out_dir = os.path.join(args.out_dir, f"{args.species}_{args.stage}")
    os.makedirs(out_dir, exist_ok=True)
    meta_path = os.path.join(out_dir, "meta.json")
    meta = {}
    if os.path.exists(meta_path):
        with open(meta_path, encoding="utf-8") as f:
            meta = json.load(f)

    for f in frames:
        prompt = APPEARANCE[key] + ", " + POSES[args.species][f]
        for seed in seeds:
            tag = f"f{f}_s{seed}"
            if tag in meta:
                print("skip existing", tag, flush=True)
                continue
            outs = queue_and_wait(build_graph(ref_name, prompt, seed))
            for o in outs.values():
                for img in o.get("images", []):
                    q = urllib.parse.urlencode({
                        "filename": img["filename"],
                        "subfolder": img.get("subfolder", ""),
                        "type": img.get("type", "output")})
                    with urllib.request.urlopen(f"{HOST}/view?{q}",
                                                timeout=60) as r:
                        data = r.read()
                    with open(os.path.join(out_dir, tag + ".png"), "wb") as fp:
                        fp.write(data)
            meta[tag] = {"frame": f, "seed": seed, "prompt": prompt,
                         "negative": NEGATIVE, "mode": "EDIT denoise=1.0",
                         "resolution": 1024, "steps": 25, "cfg": 1,
                         "sampler": "euler/simple", "ref": ref_name}
            with open(meta_path, "w", encoding="utf-8") as fp:
                json.dump(meta, fp, ensure_ascii=False, indent=1)
            print("done", tag, flush=True)
    print("ALL-DONE", key)
    return 0


if __name__ == "__main__":
    sys.exit(main())
