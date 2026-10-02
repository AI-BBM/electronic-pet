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

# 3. 组装布局目录 pets/ eggs/ 后生成 manifest（--meta 用定稿 canonical 物种表 tools/assets/species.meta.json）
python tools/assets/build_manifest.py --src <布局根目录> --meta tools/assets/species.meta.json --dst <布局根目录>/manifest.json

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

## 物种锚点与测试

- [species.meta.json](species.meta.json) 是 canonical 12 物种表（PM 定稿，
  8 common / 3 rare / 1 epic），build_manifest 的 `--meta` 一律用它，不另造种类清单；
- 测试套件：`python tools/assets/tests/run_tests.py`（33 条用例 T1–T33，
  覆盖契约功能、边界、错误路径、物种表锚点与 OSS 真实上传离线契约，
  全部通过退出码 0；对无 oss2 环境同样健壮）。

## 真实 OSS 运维手册（laoli-storage / oss-cn-beijing）

凭据纪律：AccessKey 只存在于仓库外的**凭据档案目录**（本机 `D:/AI-BBM/ecs/oss/`，
见其中 `oss_info.md` 与配对 CSV），严禁写入仓库、PR、Issue、聊天或任何脚本字面量；
工具只从环境变量读取，四个变量缺一不可：

```
OSS_ACCESS_KEY_ID / OSS_ACCESS_KEY_SECRET / OSS_ENDPOINT / OSS_BUCKET
# 本任务取值：OSS_ENDPOINT=https://oss-cn-beijing.aliyuncs.com，OSS_BUCKET=laoli-storage
# （ID/SECRET 的值从凭据档案目录加载，方式见下）
```

正式上传（素材齐备后，Git Bash 在仓库根执行）：

```bash
# 0. 加载凭据到环境变量（值不回显；从凭据档案 CSV 的两列取值）
set -a; source <凭据档案目录整理出的 env 文件>; set +a

# 1. 裁切缩放：产线 cut 图（{id}-{stage}-cut_00001.png）统一裁为长边 512（同名输出）
python tools/assets/process_assets.py --src <cut 图目录> --dst <512 输出目录>
#    再按 {id}/{stage} 改名组装布局目录 pets/{id}/1|2|3.png；剪影用 gen_silhouette 生成；
#    蛋素材放 eggs/{rarity}.png（canonical 12 物种 id 见 species.meta.json）

# 2. 组装布局目录后，生成带公网直链的正式 manifest
python tools/assets/build_manifest.py --src <布局根目录> \
  --meta tools/assets/species.meta.json \
  --url-prefix https://laoli-storage.oss-cn-beijing.aliyuncs.com \
  --dst <布局根目录>/manifest.json

# 3. 真实上传：--public-read 为 pets/、eggs/ 对象逐个设置公共读 ACL
#   （最小授权：不改桶级读写策略；mock 模式忽略此开关）
python tools/assets/upload_oss.py --src <布局根目录> --public-read
# 完成后 stdout 会打印前 3 个对象的公网 URL 样例
```

公网验收与冒烟规程：

```bash
# 验收：任一对象 HTTP 200 且 Content-Type: image/png，字节与本地一致
curl -sI "https://laoli-storage.oss-cn-beijing.aliyuncs.com/pets/cat/1.png" | grep -iE "^HTTP|^content-type"

# 冒烟（首次接通新桶时）：向 __smoke__/ 前缀上传 2 张小图 + 最小 manifest（--public-read），
# curl 校验 200/image/png/字节一致后，用 oss2 delete_object 删除冒烟对象并确认 404/403。
# 完整命令序列见仓库测试契约记录（Issue #10 测试先行子智能体产出 OPS1）。
```
