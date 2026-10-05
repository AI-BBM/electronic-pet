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

## 真实 OSS 运维手册（pet-aibbm-assets / oss-cn-hangzhou，课淘账号宠物专用公共读桶）

凭据纪律：AccessKey 只存在于仓库外的**凭据档案目录**（本机 `D:/AI-BBM/ecs/oss/`，
见其中 `oss_info.md` 与配对 CSV），严禁写入仓库、PR、Issue、聊天或任何脚本字面量；
工具只从环境变量读取，四个变量缺一不可：

```
OSS_ACCESS_KEY_ID / OSS_ACCESS_KEY_SECRET / OSS_ENDPOINT / OSS_BUCKET
# 本任务取值：OSS_ENDPOINT=https://oss-cn-hangzhou.aliyuncs.com，OSS_BUCKET=pet-aibbm-assets
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
  --url-prefix https://pet-aibbm-assets.oss-cn-hangzhou.aliyuncs.com \
  --dst <布局根目录>/manifest.json

# 3. 真实上传：--public-read 为 pets/、eggs/ 对象逐个设置公共读 ACL
#   （最小授权：不改桶级读写策略；mock 模式忽略此开关）
python tools/assets/upload_oss.py --src <布局根目录> --public-read
# 完成后 stdout 会打印前 3 个对象的公网 URL 样例
```

公网验收与冒烟规程：

```bash
# 验收：任一对象 HTTP 200 且 Content-Type: image/png，字节与本地一致
curl -sI "https://pet-aibbm-assets.oss-cn-hangzhou.aliyuncs.com/pets/cat/1.png" | grep -iE "^HTTP|^content-type"

# 冒烟（首次接通新桶时）：向 __smoke__/ 前缀上传 2 张小图 + 最小 manifest，
# curl 校验 200/image/png/字节一致后，用 oss2 delete_object 删除冒烟对象并确认 404/403。
#
# ⚠️ 目录形态（键 = 对象在 --src 内的相对路径）——要让键带 __smoke__/ 前缀，
#    必须把 __smoke__/ 作为布局根下的子目录，而不是把名为 __smoke__ 的目录本身当 --src：
#
#   <布局根>/
#     manifest.json          ← upload_oss 要求布局根必须有 manifest.json
#     __smoke__/a.png        ← 键为 __smoke__/a.png
#     __smoke__/b.png        ← 键为 __smoke__/b.png
#
#   误把 __smoke__ 目录本身当 --src 会让键（含 manifest.json）落到桶根。
#
# 冒烟命令序列：
#   python tools/assets/upload_oss.py --src <布局根>            # 先验证私有链路（公网应 403）
#   python tools/assets/upload_oss.py --src <布局根> --public-read
#   curl -sI https://<bucket>.<endpoint>/__smoke__/a.png        # 期待 200 + image/png
#   oss2 list_objects(prefix="__smoke__/") 逐个 delete_object 后复查 list 为空
```

## 挥手帧动画（#37 M9 v5，2026-10-05 定稿）

宠物 idle 动画按 v5 工艺生成：文生图模型逐帧受控生成（禁 EDIT 整图反复变形、
禁程序整帧形变），8 帧 200ms 循环动态 WebP，前端 `animOf()` 优先取
`pets/{species}/{stage}-anim.webp`（缺失回退静态 png → 剪影）。

```
OSS 静态图合成白底参考图
  → gen_wave_frames.py   ComfyUI Qwen-Image 2.1 EDIT：参考图 latent 锁形象 +
                         固定 seed 组 + 逐帧显式姿态短语，8 帧 × 多候选
  → 人工挑帧             帧间形象/色调最一致的一组；个别帧单帧重摇
  → assemble_wave.py     脚底锚定对齐 + 白底泛洪抠透明（+ 可选镜像增强挥幅）
                         → 8 帧 WebP ≤300KB + assemble_report.json（seed/
                           prompt/挑帧分/对齐数据/帧差）+ preview_sheet.png
  → upload_oss.py        覆盖上传 OSS pets/{species}/{stage}-anim.webp
```

探针定案参数（详见 gen_wave_frames.py 头注）：EDIT latent + denoise=1.0 是唯一
同时满足姿态变化/白底/帧间一致的路线；该 latent 低重绘（denoise<1）输出噪声糊，
标准 VAEEncode img2img 结构变化（抬爪）出不来。

## v6 inpaint 工艺（2026-10-05 黄总定版，覆盖 v5）

黄总验收 v5 结论"人物一致性没有保持"→ v6：**原图保底 + 仅手臂/耳/翅区域
inpaint**，身体一致性是像素级物理保证而非"看着差不多"。

```
原静态图 + 运动包络遮罩（叠图人工校准，bunny=右耳、chick=右翅/左臂）
  → inpaint_client.py    ComfyUI Qwen-Image 2.1 inpaint：VAEEncode(原图)
                         → SetLatentNoiseMask → 采样（denoise=1.0）
                         → ImageCompositeMasked 遮罩外强制回贴原图像素
  → batch_v6.py          6 只 × 2 关键位（内摆/外摆）× 2 seed
  → assemble_v6.py       帧序列 = [原图, A, B, 镜像内(A), A, 镜像内(B), B, 原图]
                         （镜像仅限遮罩内——整帧翻转会破坏遮罩外一致性）
                         → 逐帧遮罩外差值自检 <2/255 → 8 帧 200ms WebP
  → upload_oss 定向覆盖  pets/{species}/{stage}-anim.webp
```

交付证据：wave-evidence/v6/{pet}/ = mask.png（运动包络遮罩）+ 逐帧遮罩外
差值自测报告；wave-evidence/v6/meta_v6.json = 逐帧 prompt/seed/采样参数。
实测：全帧遮罩外差 0~0.27/255（限值 2），身体/脸/服饰逐像素不变。

## 角色卡 → 16 动作 → 超分 三步管线（2026-10-05 新增）

宠物动作素材的标准生产顺序（黄总定口径）：**先生成角色卡，再由角色卡派生
角色 16 个动作，最后超分放大**。角色卡是全部动作的唯一形象锚。

```
线上定稿静态图 pets/{species}/{stage}.png（512 长边）
  → gen_character_card.py  垫白 + RealESRGAN x2 预放大作参考 +
                           EDIT（denoise=1.0，固定 seed 列）生成定稿卡
                           front=正视图卡（动作锚定用）/ sheet=三视图设定卡
  → 人工选定卡 seed        后续动作全部以该卡为参考图
  → gen_actions_16.py      卡参考 latent + 显式动作短语 × 16 动作 × seed 列
                           （DEFAULT_ACTIONS 内置清单草案，--actions 可选子集）
  → 人工挑动作帧           形象/姿态不合格单动作重摇（--actions 续跑）
  → upscale_sr.py          RealESRGAN_x2plus 定稿卡/动作帧超分（x2/x4）
  → assemble 系 / OSS      抠图对齐、循环装配、覆盖上传（同 wave 工艺）
```

要点：

- 探针定案沿用 M9：EDIT latent + denoise=1.0 是姿态变化/白底/形象一致
  唯一同时满足的路线；同 seed 列保脸，跨 seed 混挑必换形象；
- 角色卡产出 `design/character_cards/{species}_{stage}/`，动作原始帧产出
  `design/actions_raw/{species}_{stage}/`，均带 meta.json（逐张 prompt/seed/
  参考卡），证据随代码入库；
- 16 动作清单目前是**草案 v1**（gen_actions_16.py 顶部 DEFAULT_ACTIONS，
  带产品映射：wave 上线问候 / cheer+jump 被加分 / sad 被扣分 / sleep 离线 /
  eat 喂食 / spin 升级进化 / bow 颁奖致谢），待 PM/黄总定稿后冻结；
- 外观锚定模板沿用 gen_wave_frames.APPEARANCE（bunny/chick 全 6 阶段手调
  定稿），新物种需补该表并连同 --card 实卡验收。
