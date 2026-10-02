# 素材规范 · assets-spec

## 生成管线（本机 ComfyUI + Qwen-Image 2.1）
1. 生成：`qwen_image_2.1_int8_convrot`（DiT）+ `qwen3vl_8b_w4a8`（文本编码器）+ `qwen_image_2.1_vae_bf16`（VAE）
   - 2048×1152，steps 25，cfg 1，euler/simple，浅灰渐变影棚背景（利于抠图）
2. 抠图：BiRefNet → `RemoveBackground` → **`InvertMask`（必须，ComfyUI 该模型输出为反向蒙版）** → `JoinImageWithAlpha`
3. 保存：`SaveImageAdvanced`（PNG / 8-bit / sRGB，保留透明通道）
4. 后处理：Python Pillow 按 alpha 包围盒裁切 → 长边缩放至 512px → 输出 web PNG

## 命名与目录（OSS）
```
pets/{species}/1.png | 2.png | 3.png     # 幼年/成长/完全体，512px 透明 PNG
eggs/{rarity}.png                        # common | rare | epic
manifest.json                            # 种类清单：id/名称/稀有度/素材 URL
```

## 风格基线（提示词锚点，全种族共用）
- 基调：3D 盲盒潮玩风格，萌系电子宠物，光滑塑料/金属外壳 + LED 发光元件 + 机械细节，居中构图，浅灰渐变影棚背景，PBR 渲染，高细节
- 允许材质变体：半透明外壳露内部结构、毛绒+LED、哑光撞色装甲
- 阶段差异：幼年=头身比大、圆润极简；成长=站立、细节适中；完全体=更大、附加装甲/光效/浮空元素

## 质量红线
- 透明背景必须为真 alpha（背景 alpha=0，主体连续不镂空）；
- 边缘无白边/黑边残留；主体完整（须/耳/尾不缺失）；
- 同一种类三阶段需可辨识为同一角色（配色/特征锚点在提示词中锁定）。
