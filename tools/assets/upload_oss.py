#!/usr/bin/env python3
"""把 OSS 布局目录上传到阿里云 OSS；凭据未配置时用 --mock 拷贝到本地目录。

用法：
    python tools/assets/upload_oss.py --src <布局根目录> --mock [--dst <本地目录>]
    python tools/assets/upload_oss.py --src <布局根目录>

- --mock：把布局目录按原结构纯字节拷贝到目标目录（默认 assets/oss-mock/，
  相对当前目录；可用 --dst 覆盖），不重新编码图片，结构与线上 OSS 完全一致；
- 真实上传：凭据读环境变量 OSS_ACCESS_KEY_ID / OSS_ACCESS_KEY_SECRET /
  OSS_ENDPOINT / OSS_BUCKET，任一缺失 → 非零退出并在写任何输出前报错（无半成品）。
"""
import argparse
import os
import shutil
import sys
from pathlib import Path

ENV_KEYS = ("OSS_ACCESS_KEY_ID", "OSS_ACCESS_KEY_SECRET", "OSS_ENDPOINT", "OSS_BUCKET")


def iter_files(root: Path):
    for path in sorted(root.rglob("*")):
        if path.is_file():
            yield path


def main() -> int:
    ap = argparse.ArgumentParser(description="上传素材布局目录到 OSS（--mock 为本地目录拷贝）")
    ap.add_argument("--src", required=True, help="布局根目录（含 manifest.json、pets/、eggs/）")
    ap.add_argument("--mock", action="store_true", help="mock 模式：拷贝到本地目录代替真实上传")
    ap.add_argument("--dst", help="mock 目标目录（默认 assets/oss-mock）；真实上传模式忽略")
    ap.add_argument(
        "--public-read",
        action="store_true",
        help="真实上传时为对象设置 public-read ACL（私有桶对外展示用）；mock 模式忽略",
    )
    args = ap.parse_args()
    src = Path(args.src)
    if not src.is_dir():
        print(f"错误：--src 不是目录：{src}", file=sys.stderr)
        return 2
    if not (src / "manifest.json").is_file():
        print(f"错误：{src} 缺少 manifest.json（先用 build_manifest.py 生成）", file=sys.stderr)
        return 2

    if args.mock:
        dst = Path(args.dst) if args.dst else Path("assets/oss-mock")
        count = 0
        for path in iter_files(src):
            target = dst / path.relative_to(src)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)
            count += 1
        print(f"mock 完成：{count} 个文件 → {dst}")
        return 0

    missing = [key for key in ENV_KEYS if not os.environ.get(key)]
    if missing:
        print(
            "错误：缺少 OSS 凭据环境变量："
            + ", ".join(missing)
            + "（真实上传需要 OSS_ACCESS_KEY_ID / OSS_ACCESS_KEY_SECRET / OSS_ENDPOINT / "
            "OSS_BUCKET；凭据未开通前请使用 --mock 本地目录模式）",
            file=sys.stderr,
        )
        return 1

    try:
        import oss2
    except ImportError:
        print("错误：oss2 未安装（pip install oss2），或改用 --mock 本地目录模式", file=sys.stderr)
        return 1

    auth = oss2.Auth(os.environ["OSS_ACCESS_KEY_ID"], os.environ["OSS_ACCESS_KEY_SECRET"])
    bucket = oss2.Bucket(auth, os.environ["OSS_ENDPOINT"], os.environ["OSS_BUCKET"])
    headers = {"x-oss-object-acl": "public-read"} if args.public_read else None
    count = 0
    for path in iter_files(src):
        key = path.relative_to(src).as_posix()
        bucket.put_object(key, path.read_bytes(), headers=headers)
        count += 1
        print(f"已上传 {key}")
    print(f"上传完成：{count} 个对象")
    host = os.environ["OSS_ENDPOINT"].split("://", 1)[-1].strip("/")
    base = f"https://{os.environ['OSS_BUCKET']}.{host}"
    keys = [p.relative_to(src).as_posix() for p in iter_files(src)]
    for key in keys[:3]:
        print(f"{base}/{key}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
