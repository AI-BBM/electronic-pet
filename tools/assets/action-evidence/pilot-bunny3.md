# 试点报告：角色卡 → 动作 → 超分 三步管线（bunny_3，2026-10-05）

口径（黄总）：宠物先生成角色卡，再由角色卡派生角色 16 个动作，最后超分放大。
本试点用 M9 试点物种云绒兔（完全体 bunny_3）打通全链并出样张。

## 产出物

| 步骤 | 文件 | 说明 |
| --- | --- | --- |
| 1 角色卡 | design/character_cards/bunny_3/card_front_s{68,101}.png | 640×1024 白底定稿卡，**选定 s68**（双耳对称、心形灯完整、构图标准） |
| 1b 三视图卡 | （脚本支持 --mode sheet，本次未出样） | 设定集/评审用 |
| 2 动作 | design/actions_raw/bunny_3/{jump,cheer,sleep,eat}_s{68,101}.png | 4 探针动作 ×2 seed；contact_sheet.png 对比图 |
| 3 超分 | design/character_cards/bunny_3/sr/card_front_s68_sr.png<br>design/actions_raw/bunny_3/sr/jump_s68_sr.png | RealESRGAN_x2plus → 1280×2048，绒毛细节清晰无伪影 |

逐张 prompt/seed/参考卡证据：两目录下 meta.json。

## 质检结论

- s68 列四动作（jump/cheer/sleep/eat）形象一致性全部达标：心形灯耳、
  白耳机、腮红、奶油绒毛全部保持——同 seed 列保脸再次验证（M9 结论沿用）；
- s101 列两处漂移：cheer 眼睛变紫色星云眼（眼饰风格漂移）、sleep 闭眼失败
  （睁眼睡觉）——双 seed 挑帧机制正是为此设计，逐动作挑优；
- jump 全身腾空、cheer 双爪上举、sleep 蜷坐（模型把提示词里的 z 气泡画成
  小爱心，语义可接受）、eat 捧食啃咬，姿态全部正确；
- eat 两张出现「耳机线缆」细节增生（把颈挂耳机画成了入耳式线控），
  放量前需在动作短语中加负面约束。

## 成本实测（RTX 4070 12G，独占队列）

- EDIT 640×1024 / 25 步：约 60~75s/张；RealESRGAN x2：约 20s/张；
- 全量成本估算：12 物种 × 3 阶段 = 36 卡 + 576 动作 ×2 seed ≈ 1224 张
  ≈ **24h GPU**；仅完全体（12 卡 + 192 动作 ×2 seed）≈ **4.5h GPU**。

## 放量前待定稿（PM/黄总）

1. **16 动作清单**：gen_actions_16.py 顶部 DEFAULT_ACTIONS 为草案 v1
   （idle/wave/jump/cheer/walk/run/sit/sleep/eat/dance/nod/shake/sad/clap/
   bow/spin，产品映射见脚本注释）——需定稿冻结；
2. **放量范围**：全 12 物种 × 3 阶段，还是先完全体（推荐：先 2 试点物种
   全阶段，再全量完全体，最后补齐 1/2 阶段）；
3. **卡型**：正视图卡（动作锚定，本试点采用）是否需要补三视图设定卡；
4. 动作循环装配沿用 M9 v6 工艺（原图保底+遮罩 inpaint）还是直接用
   单帧动作序列拼循环，待首轮 16 动作全样张评审后定。

## 复跑命令

```bash
# 1. 角色卡（源图先垫白 + RealESRGAN x2 预放大，脚本内自动完成）
python tools/assets/gen_character_card.py --species bunny --stage 3 \
  --src design/assets_output/pets/bunny/3.png --mode front --seeds 68,101
# 2. 动作（--card 指定选定卡）
python tools/assets/gen_actions_16.py --species bunny --stage 3 \
  --card design/character_cards/bunny_3/card_front_s68.png \
  --actions jump,cheer --seeds 68,101
# 3. 超分
python tools/assets/upscale_sr.py --src <png或目录> --dst <输出目录>
```
