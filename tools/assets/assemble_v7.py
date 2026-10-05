#!/usr/bin/env python3
"""v7 ③：裁剪 → RealESRGAN x2 超分 → 回落 512 高 → 抠透明 → 16 帧 200ms WebP。

用法：python assemble_v7.py --pet bunny_1 --picks f04:101,f05:68 ...
挑帧：默认取 s68；--picks 覆盖个别帧。装配后输出 preview + meta 记录。
"""
import argparse
import json
import os
import sys
import urllib.parse
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from inpaint_client import api
from PIL import Image, ImageChops, ImageDraw, ImageFilter

BASE = r"C:/Users/hxx/AppData/Local/Temp/m9v7"
OUT_H = 512
MS = 200
MAX_BYTES = 300 * 1024


def crop_subject(img: Image.Image, white_t: int = 26, pad: int = 8):
    r, g, b = img.split()[:3]
    mn = ImageChops.darker(ImageChops.darker(r, g), b)
    dist = ImageChops.invert(mn)
    core = dist.point(lambda v: 255 if v >= white_t else 0)
    bbox = core.getbbox()
    if not bbox:
        return img
    x0, y0, x1, y1 = bbox
    x0 = max(0, x0 - pad)
    y0 = max(0, y0 - pad)
    x1 = min(img.width, x1 + pad)
    y1 = min(img.height, y1 + pad)
    return img.crop((x0, y0, x1, y1))


def esrgan_x2(img: Image.Image, tag: str):
    """ComfyUI ImageUpscaleWithModel(RealESRGAN_x2plus)。"""
    up_in = os.path.join(BASE, "out", "sr_in")
    up_out = os.path.join(BASE, "out", "sr_out")
    os.makedirs(up_in, exist_ok=True)
    os.makedirs(up_out, exist_ok=True)
    p = os.path.join(up_in, tag + ".png")
    img.save(p)

    # 上传裁剪图：走 inpaint_client.upload（multipart）
    from inpaint_client import upload
    up = upload(p)["name"]
    graph = {
        "1": {"class_type": "UpscaleModelLoader",
              "inputs": {"model_name": "RealESRGAN_x2plus.pth"}},
        "2": {"class_type": "LoadImage", "inputs": {"image": up}},
        "3": {"class_type": "ImageUpscaleWithModel",
              "inputs": {"upscale_model": ["1", 0], "image": ["2", 0]}},
        "4": {"class_type": "SaveImage",
              "inputs": {"images": ["3", 0], "filename_prefix": f"sr_{tag}"}},
    }
    import json as _json
    import time as _time
    pid = api("/prompt", {"prompt": graph, "client_id": "v7sr"})["prompt_id"]
    t0 = _time.time()
    while _time.time() - t0 < 600:
        h = api(f"/history/{pid}")
        if pid in h:
            st = h[pid].get("status", {})
            if st.get("status_str") == "error":
                raise RuntimeError(_json.dumps(st, ensure_ascii=False)[:500])
            if st.get("completed") or h[pid].get("outputs"):
                outs = h[pid]["outputs"]
                break
        _time.sleep(2)
    else:
        raise TimeoutError(pid)
    for o in outs.values():
        for imgo in o.get("images", []):
            q = urllib.parse.urlencode({
                "filename": imgo["filename"], "subfolder": imgo.get("subfolder", ""),
                "type": imgo.get("type", "output")})
            with urllib.request.urlopen(
                    f"http://127.0.0.1:8188/view?{q}", timeout=120) as resp:
                data = resp.read()
            out_p = os.path.join(up_out, tag + ".png")
            with open(out_p, "wb") as f:
                f.write(data)
            return Image.open(out_p).convert("RGB")
    raise RuntimeError("no sr output")


def to_rgba(img: Image.Image):
    r, g, b = img.split()[:3]
    mn = ImageChops.darker(ImageChops.darker(r, g), b)
    dist = ImageChops.invert(mn)
    near = dist.point(lambda v: 255 if v < 26 else 0)
    ff = near.copy()
    W, H = ff.size
    for xy in ((0, 0), (W-1, 0), (0, H-1), (W-1, H-1), (W//2, 0), (W//2, H-1)):
        if ff.getpixel(xy) == 255:
            ImageDraw.floodfill(ff, xy, 128)
    bgm = ff.point(lambda v: 255 if v == 128 else 0)
    fg = ImageChops.invert(bgm)
    core = dist.point(lambda v: 255 if v >= 26 else 0)
    m = ImageChops.multiply(fg, core).filter(
        ImageFilter.MaxFilter(5)).filter(ImageFilter.MinFilter(5))
    out = img.convert("RGBA")
    out.putalpha(m.filter(ImageFilter.GaussianBlur(1.0)))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pet", required=True)
    ap.add_argument("--picks", help="f04:101,f05:68（覆盖默认 s68）")
    ap.add_argument("--out-h", type=int, default=OUT_H, help="成品帧高（超 300KB 时调低）")
    args = ap.parse_args()
    pet = args.pet
    picks = {}
    if args.picks:
        for kv in args.picks.split(","):
            k, val = kv.replace("f", "").split(":")
            picks[int(k)] = int(val)

    adir = os.path.join(BASE, "out", "actions")
    frames = []
    chosen = {}
    for f in range(16):
        seed = picks.get(f, 68)
        p = os.path.join(adir, f"{pet}_f{f:02d}_s{seed}.png")
        if not os.path.exists(p):
            print("缺帧", p, file=sys.stderr)
            return 2
        chosen[f] = seed
        frames.append(Image.open(p).convert("RGB"))

    # ① 裁剪：逐帧主体 bbox（pad 8），不做强 resize（避免宽高比变形）
    cropped = [crop_subject(im) for im in frames]

    # ② ESRGAN ×2（逐帧）
    sr = [esrgan_x2(c, f"{pet}_f{f:02d}") for f, c in enumerate(cropped)]

    # ③ 脚底锚定对齐：以 f0 主体高度为基准，各帧等比缩放到同高，
    #    底边中心对齐贴到 f0 画布（抬臂=纯向上运动，身体不变形）
    def subj_h(im):
        r, g, b = im.split()[:3]
        mn = ImageChops.darker(ImageChops.darker(r, g), b)
        core = ImageChops.invert(mn).point(lambda v: 255 if v >= 26 else 0)
        bb = core.getbbox()
        return (bb[3] - bb[1], bb) if bb else (im.height, (0, 0, im.width, im.height))

    h0, _ = subj_h(sr[0])
    canvas_w, canvas_h = sr[0].size
    aligned = []
    for im in sr:
        h, _ = subj_h(im)
        s = min(1.15, max(0.87, h0 / h))
        if abs(s - 1.0) > 0.01:
            im = im.resize((int(im.width * s), int(im.height * s)), Image.LANCZOS)
        canvas = Image.new("RGB", (canvas_w, canvas_h), (255, 255, 255))
        canvas.paste(im, ((canvas_w - im.width) // 2, canvas_h - im.height))
        aligned.append(canvas)

    # ④ 回落目标高 + 抠透明
    scale = args.out_h / canvas_h
    final = []
    for im in aligned:
        im2 = im.resize((int(canvas_w * scale), OUT_H), Image.LANCZOS)
        final.append(to_rgba(im2))

    out_path = os.path.join(BASE, "out", f"{pet}-anim.webp")
    q = 85
    while True:
        final[0].save(out_path, save_all=True, append_images=final[1:],
                      duration=MS, loop=0, format="WEBP", quality=q, method=6)
        size = os.path.getsize(out_path)
        if size <= MAX_BYTES or q <= 28:
            break
        q -= 10
    print(f"written {out_path}: 16 frames, {size} bytes (q={q})")

    report = {"pet": pet, "frames": 16, "ms": MS, "picks": chosen,
              "canvas": [canvas_w, canvas_h], "f0_subject_h": h0,
              "bytes": size, "quality": q,
              "sr": "RealESRGAN_x2plus", "align": "feet-anchored"}
    with open(os.path.join(BASE, "out", f"report_v7_{pet}.json"), "w",
              encoding="utf-8") as fp:
        json.dump(report, fp, ensure_ascii=False, indent=1)

    sheet = Image.new("RGB", (final[0].width * 8 + 70, final[0].height * 2 + 20),
                      (255, 255, 255))
    x = 5
    for i, fr in enumerate(final):
        bg = Image.new("RGB", fr.size, (255, 255, 255))
        bg.paste(fr, (0, 0), fr)
        col = i % 8
        row = i // 8
        sheet.paste(bg, (5 + col * (fr.width + 10), 5 + row * (fr.height + 10)))
    sheet.save(os.path.join(BASE, "out", f"preview_v7_{pet}.png"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
