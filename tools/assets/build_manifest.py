#!/usr/bin/env python3
"""扫描 OSS 布局目录，生成 manifest.json（种类清单）。

布局契约（docs/product/assets-spec.md）：
    pets/{species}/1.png | 2.png | 3.png | silhouette.png
    eggs/{rarity}.png                      # common | rare | epic

用法：
    python tools/assets/build_manifest.py --src <布局根目录> --meta <种类元数据.json> --dst <manifest.json>

--meta 格式：{"<id>": {"name": "名称", "rarity": "common|rare|epic"}}
生成前做严格校验：缺阶段图 / 缺剪影 / 缺蛋素材 / 稀有度非法 → 整体非零退出
（stderr 逐条点名），不写出半成品 manifest。
"""
import argparse
import json
import sys
from pathlib import Path

RARITIES = ("common", "rare", "epic")
STAGES = ("1", "2", "3")


def main() -> int:
    ap = argparse.ArgumentParser(description="由 OSS 布局目录生成 manifest.json")
    ap.add_argument("--src", required=True, help="布局根目录（含 pets/ 与 eggs/）")
    ap.add_argument("--meta", required=True, help="种类元数据 JSON：{id: {name, rarity}}")
    ap.add_argument("--dst", required=True, help="manifest.json 输出路径")
    ap.add_argument("--version", default="1", help="manifest 版本号（默认 1）")
    ap.add_argument(
        "--url-prefix",
        default=None,
        help="公网 URL 基址（如 https://<bucket>.oss-cn-beijing.aliyuncs.com），"
        "给出时所有 URL = 基址 + 相对键；缺省输出相对键",
    )
    args = ap.parse_args()
    src, dst = Path(args.src), Path(args.dst)
    if not src.is_dir():
        print(f"错误：--src 不是目录：{src}", file=sys.stderr)
        return 2

    meta = json.loads(Path(args.meta).read_text(encoding="utf-8"))
    if not isinstance(meta, dict):
        print("错误：--meta 顶层必须是对象 {id: {name, rarity}}", file=sys.stderr)
        return 2

    errors = []

    eggs = {}
    for rarity in RARITIES:
        rel = f"eggs/{rarity}.png"
        if (src / rel).is_file():
            eggs[rarity] = rel
        else:
            errors.append(f"缺少蛋素材：{rel}（egg）")

    species = []
    for sp_id in sorted(meta):
        info = meta[sp_id]
        if not isinstance(info, dict):
            errors.append(f"种类 {sp_id}：meta 项必须是对象 {{name, rarity}}")
            continue
        name = info.get("name")
        rarity = info.get("rarity")
        if not name:
            errors.append(f"种类 {sp_id}：meta 缺少 name")
        if rarity not in RARITIES:
            errors.append(
                f"种类 {sp_id}：rarity 非法（{rarity!r}），须为 {'/'.join(RARITIES)}"
            )

        stages = {}
        for k in STAGES:
            rel = f"pets/{sp_id}/{k}.png"
            if (src / rel).is_file():
                stages[k] = rel
            else:
                errors.append(f"种类 {sp_id}：缺少阶段图 {rel}")

        sil_rel = f"pets/{sp_id}/silhouette.png"
        if not (src / sil_rel).is_file():
            errors.append(f"种类 {sp_id}：缺少剪影 {sil_rel}")

        species.append(
            {
                "id": sp_id,
                "name": name,
                "rarity": rarity,
                "stages": stages,
                "silhouette": sil_rel,
            }
        )

    if errors:
        for line in errors:
            print(f"错误：{line}", file=sys.stderr)
        print(f"共 {len(errors)} 处校验失败，未生成 manifest", file=sys.stderr)
        return 1

    prefix = args.url_prefix.rstrip("/") if args.url_prefix else None

    def to_url(rel: str) -> str:
        return f"{prefix}/{rel}" if prefix else rel

    manifest = {
        "version": str(args.version),
        "eggs": {r: to_url(rel) for r, rel in eggs.items()},
        "species": [
            {
                "id": sp["id"],
                "name": sp["name"],
                "rarity": sp["rarity"],
                "stages": {k: to_url(rel) for k, rel in sp["stages"].items()},
                "silhouette": to_url(sp["silhouette"]),
            }
            for sp in species
        ],
    }
    dst.parent.mkdir(parents=True, exist_ok=True)
    dst.write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    print(f"manifest 已生成：{dst}（{len(species)} 个种类）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
