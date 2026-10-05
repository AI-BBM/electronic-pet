#!/usr/bin/env python3
"""#37 v6 inpaint 探针与生产客户端：原图保底 + 仅手臂/翅膀包络重绘。

v6 工艺（PM 2026-10-05，覆盖 v5 整帧重绘）：
① 站姿帧 = 原静态图像素原封不动（零生成）；
② 挥动帧只重绘遮罩内（手臂/翅膀运动包络），遮罩外经 Composite 节点强制回贴
   原图像素——脸/蛋壳/头盔/躯干/脚零变化是物理保证；
③ 手臂 2~3 关键位 + 镜像组合成 8 帧循环，200ms、≤300KB；
④ 逐帧与原图遮罩外平均像素差 <2/255 自测，交付附遮罩 PNG + 自测值表。
"""
import json
import os
import time
import urllib.parse
import urllib.request

HOST = "http://127.0.0.1:8188"

NEG = ("background scene, gradient background, colored background, floor shadow, "
       "multiple characters, deformed, extra limbs, blurry, text, watermark")


def api(path, data=None):
    req = urllib.request.Request(
        HOST + path,
        data=None if data is None else json.dumps(data).encode(),
        headers={} if data is None else {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read().decode())


def upload(path):
    import mimetypes
    boundary = "----v6boundary"
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


def build_graph(orig_name, mask_name, prompt, seed, denoise=1.0, steps=25):
    """inpaint：原图 latent + 遮罩定向噪声 + 遮罩外像素回贴（Composite）。"""
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
        "6": {"class_type": "LoadImage", "inputs": {"image": orig_name}},
        "8": {"class_type": "LoadImageMask",
              "inputs": {"image": mask_name, "channel": "red"}},
        "14": {"class_type": "VAEEncode",
               "inputs": {"pixels": ["6", 0], "vae": ["5", 0]}},
        "15": {"class_type": "SetLatentNoiseMask",
               "inputs": {"samples": ["14", 0], "mask": ["8", 0]}},
        "16": {"class_type": "TextEncodeQwenImage21",
               "inputs": {"clip": ["4", 0], "images": [["6", 0]], "vae": ["5", 0],
                          "prompt": prompt, "negative_prompt": NEG,
                          "resolution": 512}},
        "11": {"class_type": "KSampler",
               "inputs": {"model": ["3", 0], "positive": ["16", 0],
                          "negative": ["16", 1], "latent_image": ["15", 0],
                          "seed": seed, "steps": steps, "cfg": 1,
                          "sampler_name": "euler", "scheduler": "simple",
                          "denoise": denoise}},
        "12": {"class_type": "VAEDecode",
               "inputs": {"samples": ["11", 0], "vae": ["5", 0]}},
        "20": {"class_type": "ImageCompositeMasked",
               "inputs": {"destination": ["6", 0], "source": ["12", 0],
                          "x": 0, "y": 0, "resize_source": False,
                          "mask": ["8", 0]}},
        "21": {"class_type": "SaveImage",
               "inputs": {"images": ["12", 0], "filename_prefix": "v6_raw"}},
        "22": {"class_type": "SaveImage",
               "inputs": {"images": ["20", 0], "filename_prefix": "v6_comp"}},
    }


def run(graph, timeout=1200):
    pid = api("/prompt", {"prompt": graph, "client_id": "v6"})["prompt_id"]
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


def fetch(outs, dest):
    os.makedirs(dest, exist_ok=True)
    saved = []
    for o in outs.values():
        for img in o.get("images", []):
            q = urllib.parse.urlencode({
                "filename": img["filename"], "subfolder": img.get("subfolder", ""),
                "type": img.get("type", "output")})
            with urllib.request.urlopen(f"{HOST}/view?{q}", timeout=60) as r:
                data = r.read()
            path = os.path.join(dest, img["filename"])
            with open(path, "wb") as f:
                f.write(data)
            saved.append(path)
    return saved
