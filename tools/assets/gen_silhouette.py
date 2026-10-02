#!/usr/bin/env python3
"""生成未解锁剪影：形状（alpha 通道）逐像素不变，可见像素统一为纯黑。

用法：
    python tools/assets/gen_silhouette.py --src <PNG 文件或目录> --dst <输出目录>

规格：
- --src 为目录时批处理其顶层全部文件，为文件时处理该文件；输出保留原文件名；
- 输出 RGBA、尺寸与输入一致；RGB 三通道全置 (0,0,0)，alpha 逐像素等于输入；
- 任一文件无法解码、无 alpha 通道或全透明 → 整体非零退出，
  stderr 逐条点名文件与原因，不静默跳过。
"""
import argparse
import sys
from pathlib import Path

from PIL import Image, UnidentifiedImageError


def silhouette_one(path: Path) -> Image.Image:
    try:
        im = Image.open(path)
        im.load()
    except (UnidentifiedImageError, OSError) as exc:
        raise ValueError(f"{path.name}: 无法作为图片解码（{exc}）")
    if "A" not in im.getbands() and not (im.mode == "P" and "transparency" in im.info):
        raise ValueError(f"{path.name}: 无 alpha 通道（mode={im.mode}），需要透明 PNG")
    rgba = im.convert("RGBA")
    out = Image.new("RGBA", rgba.size, (0, 0, 0, 0))
    out.putalpha(rgba.getchannel("A"))
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description="透明 PNG 生成未解锁剪影（纯黑、alpha 不变）")
    ap.add_argument("--src", required=True, help="输入 PNG 文件或目录")
    ap.add_argument("--dst", required=True, help="输出目录（保留原文件名，自动创建）")
    args = ap.parse_args()
    src, dst = Path(args.src), Path(args.dst)
    if not src.exists():
        print(f"错误：--src 不存在：{src}", file=sys.stderr)
        return 2
    if src.is_dir():
        inputs = sorted(p for p in src.iterdir() if p.is_file())
    else:
        inputs = [src]
    dst.mkdir(parents=True, exist_ok=True)
    errors = []
    for path in inputs:
        try:
            silhouette_one(path).save(dst / path.name, "PNG")
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
