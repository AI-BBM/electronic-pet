#!/usr/bin/env python3
"""超分放大客户端（管线第 3 步）：RealESRGAN_x2plus 经本机 ComfyUI 放大。

角色卡 / 动作帧定稿后的统一放大出口：白底原图 x2 → 长边约 2048，供裁切
（process_assets 长边 512 不放大的口径不变，超分产物供定稿卡与高清演出用）。
透明 PNG 自动先垫白（RealESRGAN 不认 alpha）。

用法：
  python upscale_sr.py --src <PNG 或目录> --dst <输出目录> [--scale 2]
"""
import argparse
import json
import os
import sys
import time
import urllib.parse
import urllib.request

HOST = "http://127.0.0.1:8188"
MODEL = "RealESRGAN_x2plus.pth"


def api(path, data=None):
    req = urllib.request.Request(
        HOST + path,
        data=None if data is None else json.dumps(data).encode(),
        headers={} if data is None else {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read().decode())


def upload_image(path):
    import mimetypes
    boundary = "----srboundary"
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
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.loads(r.read().decode())


def build_graph(ref_name, scale):
    """LoadImage → RealESRGAN 模型放大 →（可选二次放大）→ SaveImage。"""
    graph = {
        "2": {"class_type": "LoadImage", "inputs": {"image": ref_name}},
        "3": {"class_type": "UpscaleModelLoader", "inputs": {"model_name": MODEL}},
        "4": {"class_type": "ImageUpscaleWithModel",
              "inputs": {"upscale_model": ["3", 0], "image": ["2", 0]}},
        "5": {"class_type": "SaveImage",
              "inputs": {"images": ["4", 0], "filename_prefix": "sr"}},
    }
    if scale >= 4:
        # x2 模型级联两遍得到 x4
        graph["6"] = {"class_type": "ImageUpscaleWithModel",
                      "inputs": {"upscale_model": ["3", 0], "image": ["4", 0]}}
        graph["5"]["inputs"]["images"] = ["6", 0]
    return graph


def queue_and_wait(graph, timeout=1800):
    pid = api("/prompt", {"prompt": graph, "client_id": "sr"})["prompt_id"]
    t0 = time.time()
    while time.time() - t0 < timeout:
        h = api(f"/history/{pid}")
        if pid in h:
            status = h[pid].get("status", {})
            if status.get("status_str") == "error":
                raise RuntimeError(json.dumps(status, ensure_ascii=False)[:800])
            if status.get("completed") or h[pid].get("outputs"):
                return h[pid]["outputs"]
        time.sleep(2)
    raise TimeoutError(pid)


def flatten_white(src, dst):
    from PIL import Image
    im = Image.open(src)
    if im.mode in ("RGBA", "LA", "P"):
        im = im.convert("RGBA")
        base = Image.new("RGBA", im.size, (255, 255, 255, 255))
        base.alpha_composite(im)
        base.convert("RGB").save(dst)
    else:
        im.convert("RGB").save(dst)


def run_one(src, dst_dir, scale):
    from PIL import Image
    tmp = src + ".white.png"
    flatten_white(src, tmp)
    ref = upload_image(tmp)["name"]
    os.remove(tmp)
    outs = queue_and_wait(build_graph(ref, scale))
    os.makedirs(dst_dir, exist_ok=True)
    saved = []
    for o in outs.values():
        for img in o.get("images", []):
            q = urllib.parse.urlencode({
                "filename": img["filename"], "subfolder": img.get("subfolder", ""),
                "type": img.get("type", "output")})
            with urllib.request.urlopen(f"{HOST}/view?{q}", timeout=120) as r:
                data = r.read()
            dst = os.path.join(dst_dir, os.path.splitext(os.path.basename(src))[0]
                               + "_sr.png")
            with open(dst, "wb") as f:
                f.write(data)
            w, h = Image.open(dst).size
            saved.append(dst)
            print(f"sr ok {dst} {w}x{h}", flush=True)
    return saved


def main():
    ap = argparse.ArgumentParser(description="RealESRGAN x2 超分（ComfyUI）")
    ap.add_argument("--src", required=True, help="输入 PNG 或目录")
    ap.add_argument("--dst", required=True, help="输出目录")
    ap.add_argument("--scale", type=int, choices=[2, 4], default=2)
    args = ap.parse_args()

    srcs = []
    if os.path.isdir(args.src):
        srcs = sorted(os.path.join(args.src, f) for f in os.listdir(args.src)
                      if f.lower().endswith(".png"))
    elif os.path.exists(args.src):
        srcs = [args.src]
    if not srcs:
        print("错误：无输入 PNG", args.src, file=sys.stderr)
        return 2
    for s in srcs:
        run_one(s, args.dst, args.scale)
    print("ALL-DONE", len(srcs), "file(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
