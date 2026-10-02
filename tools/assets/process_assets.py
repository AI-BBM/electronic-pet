#!/usr/bin/env python3
"""素材后处理：按 alpha 包围盒裁切 + 长边缩放至 512px。

对应 docs/product/assets-spec.md「生成管线」第 4 步：
    Python Pillow 按 alpha 包围盒裁切 → 长边缩放至 512px → 输出 web PNG

用法：
    python tools/assets/process_assets.py --src <输入目录> --dst <输出目录>

规格：
- 处理 --src 顶层全部文件，输出同名文件到 --dst（内容一律为 PNG 格式，保留 RGBA）；
- 先按 alpha>0 的包围盒裁切；裁切后长边 >512 才等比缩放到长边=512，
  长边 ≤512 保持原尺寸不放大；
- 任一文件无法解码、无 alpha 通道或全透明 → 整体非零退出，
  stderr 逐条点名文件与原因，不静默跳过。
"""
import argparse
import sys
from pathlib import Path

from PIL import Image, UnidentifiedImageError

MAX_EDGE = 512


def process_one(path: Path) -> Image.Image:
    try:
        im = Image.open(path)
        im.load()
    except (UnidentifiedImageError, OSError) as exc:
        raise ValueError(f"{path.name}: 无法作为图片解码（{exc}）")
    if "A" not in im.getbands() and not (im.mode == "P" and "transparency" in im.info):
        raise ValueError(f"{path.name}: 无 alpha 通道（mode={im.mode}），需要透明 PNG")
    im = im.convert("RGBA")
    bbox = im.getchannel("A").getbbox()
    if bbox is None:
        raise ValueError(f"{path.name}: 全透明（alpha 包围盒为空），缺少主体")
    im = im.crop(bbox)
    long_edge = max(im.size)
    if long_edge > MAX_EDGE:
        scale = MAX_EDGE / long_edge
        im = im.resize((round(im.width * scale), round(im.height * scale)), Image.LANCZOS)
    return im


def main() -> int:
    ap = argparse.ArgumentParser(description="透明 PNG 批量裁切缩放（长边 512px，不放大）")
    ap.add_argument("--src", required=True, help="输入目录（顶层文件逐一处理）")
    ap.add_argument("--dst", required=True, help="输出目录（同名输出，自动创建）")
    args = ap.parse_args()
    src, dst = Path(args.src), Path(args.dst)
    if not src.is_dir():
        print(f"错误：--src 不是目录：{src}", file=sys.stderr)
        return 2
    dst.mkdir(parents=True, exist_ok=True)
    errors = []
    for path in sorted(src.iterdir()):
        if not path.is_file():
            continue
        try:
            process_one(path).save(dst / path.name, "PNG")
        except ValueError as exc:
            errors.append(str(exc))
    if errors:
        for line in errors:
            print(f"错误：{line}", file=sys.stderr)
        print(f"共 {len(errors)} 个文件处理失败，未通过校验的文件未输出", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
