# 素材接入工具链（tools/assets）

按 [docs/product/assets-spec.md](../../docs/product/assets-spec.md) 搭建的素材后处理与索引管线。
生成端（ComfyUI + Qwen-Image 2.1）产出透明 PNG 后，由本目录脚本完成裁切缩放、
剪影生成、manifest 索引与 OSS 上传。

## 管线总览

```
生成端透明 PNG
  → process_assets.py   alpha 包围盒裁切 + 长边 512px（不放大）
  → gen_silhouette.py   未解锁剪影（纯黑、alpha 不变）
  → 组装 OSS 布局目录（pets/{species}/1|2|3|silhouette.png、eggs/{rarity}.png）
  → build_manifest.py   生成 manifest.json（严格校验，失败不产出）
  → upload_oss.py       上传 OSS（凭据未配置时 --mock 拷贝到 assets/oss-mock/）
```

OSS 目录契约：

```
pets/{species}/1.png | 2.png | 3.png     # 幼年/成长/完全体，长边 512px 透明 PNG
pets/{species}/silhouette.png            # 未解锁剪影
eggs/{rarity}.png                        # common | rare | epic
manifest.json                            # 种类清单，结构见 manifest.schema.json
```

## 用法

```bash
# 1. 裁切缩放：输入目录顶层文件逐一处理，输出同名 PNG
python tools/assets/process_assets.py --src <原始透明PNG目录> --dst <输出目录>

# 2. 剪影：文件或目录均可，输出保留原文件名
python tools/assets/gen_silhouette.py --src <透明PNG或目录> --dst <剪影输出目录>

# 3. 组装布局目录 pets/ eggs/ 后生成 manifest（种类元数据：{id: {name, rarity}}）
python tools/assets/build_manifest.py --src <布局根目录> --meta <meta.json> --dst <布局根目录>/manifest.json

# 4. 上传：OSS 凭据未开通前用 --mock 拷贝到本地 assets/oss-mock/（结构与线上一致）
python tools/assets/upload_oss.py --src <布局根目录> --mock
```

## 校验与错误行为

- 三个处理脚本对坏输入（无法解码、无 alpha 通道、全透明）整体非零退出，
  stderr 逐条点名文件与原因，不静默跳过；
- build_manifest.py 校验缺阶段图 / 缺剪影 / 缺蛋素材 / 稀有度非法，失败不写出 manifest；
- upload_oss.py 真实模式凭据缺失时在任何输出发生前失败（无半成品目录）。

## manifest 结构

顶层 `{version, eggs, species}`；`species[]` 元素 `{id, name, rarity, stages, silhouette}`，
URL 为以布局相对键结尾的字符串（可带 CDN/OSS 前缀）。完整定义见
[manifest.schema.json](manifest.schema.json)。前端占位 manifest 允许 stages 只含部分
阶段、silhouette/eggs 取值可空（`null`），与真实管线全量输出同构，可直接替换。
