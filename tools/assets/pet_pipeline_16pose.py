#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
宠物资产端到端工业管线 (Pet Asset 3-Stage Pipeline)
流程规范：
  阶段 1: 生成/锁定宠物角色卡片 (Character Reference Sheet / Card)
  阶段 2: 根据角色卡片作为唯一参考图，生成 4×4 连贯 16 动作姿态表 (16-Pose Sheet)
  阶段 3: 调用超分模型 (RealESRGAN_x2plus) 放大，自动切片出 16 张 512px 高清独立关键帧并生成动图预览

支持与本机 ComfyUI (127.0.0.1:8188) 深度集成，实现全自动端到端执行。
"""

import argparse
import json
import mimetypes
import os
import sys
import time
import urllib.parse
import urllib.request
from PIL import Image

COMFY_HOST = "http://127.0.0.1:8188"
UPSCALE_MODEL_NAME = "RealESRGAN_x2plus.pth"

NEGATIVE_PROMPT = (
    "background scene, realistic human, ugly, blurry, text, watermark, bad anatomy, "
    "deformed, extra limbs, bad proportions, bad eyes, disfigured, cut off"
)

# 16 动作编排公式（Issue #44 定稿 v1：黄总叙事「亮相→歪头→蓄力→挥手三连→蹦跳→大跳→收官」）
PET_16_ACTIONS = [
    "01. 开场亮相 (Opening Pose): standing naturally and relaxed facing forward, establishing adorable character identity",
    "02. 律动歪头·左 (Head Tilt Left): tilting head playfully to the left with big curious eyes",
    "03. 律动歪头·右 (Head Tilt Right): tilting head cheerfully to the right with sparkling happy expression",
    "04. 蓄力下蹲 (Crouch Ready): crouching down low with lowered center of gravity, preparing to spring up",
    "05. 抬爪预备 (Paw Lift): standing on hind feet, lifting front paw/wing halfway, preparing to wave",
    "06. 挥手·爪举头侧 (Wave Ready): right front paw/wing raised upright beside its head, open paw ready to wave (wave keyframe 1)",
    "07. 挥手·挥左 (Wave Left): right front paw/wing waving tilted towards the left (wave keyframe 2)",
    "08. 挥手·挥右 (Wave Right): right front paw/wing waving tilted towards the right (wave keyframe 3)",
    "09. 开心蹦跳起 (Bounce Takeoff): springing upward happily with feet just leaving the ground",
    "10. 腾空大跳·最高点 (Climactic Apex Jump): leaping high at apex of jump in mid-air, joyful triumphant celebration",
    "11. 落地缓冲 (Landing Cushion): landing softly on the ground, bending knees to absorb the landing comfortably",
    "12. 满意点头 (Approving Nod): nodding head down once politely and happily with an earnest approving smile",
    "13. 原地小碎步 (Shuffle Steps): cute rapid shuffle steps in place, shifting weight playfully from foot to foot",
    "14. 转身回望 (Turn & Glance Back): turning body into three-quarter rear angle, glancing back cutely over shoulder",
    "15. 收势站定 (Settle Stance): turning back to front, clean composed standing posture",
    "16. 眨眼微笑收官 (Wink & Smile Finale): confident front facing pose, winking one eye cheerfully with a bright finishing smile"
]


def api_post(path, data=None):
    url = COMFY_HOST + path
    body = None if data is None else json.dumps(data).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": "application/json"} if data else {}
    )
    with urllib.request.urlopen(req, timeout=60) as res:
        return json.loads(res.read().decode("utf-8"))


def api_get(path):
    url = COMFY_HOST + path
    req = urllib.request.Request(url)
    with urllib.request.urlopen(req, timeout=60) as res:
        return json.loads(res.read().decode("utf-8"))


def upload_to_comfy(image_path):
    """上传本地图片到 ComfyUI input 目录"""
    boundary = "----PetPipelineBoundary"
    fname = os.path.basename(image_path)
    with open(image_path, "rb") as f:
        file_bytes = f.read()

    body = b"".join([
        f"--{boundary}\r\n".encode(),
        f'Content-Disposition: form-data; name="image"; filename="{fname}"\r\n'.encode(),
        f"Content-Type: {mimetypes.guess_type(fname)[0] or 'image/png'}\r\n\r\n".encode(),
        file_bytes, b"\r\n",
        f"--{boundary}\r\n".encode(),
        b'Content-Disposition: form-data; name="overwrite"\r\n\r\ntrue\r\n',
        f"--{boundary}\r\n".encode(),
        b'Content-Disposition: form-data; name="type"\r\n\r\ninput\r\n',
        f"--{boundary}--\r\n".encode(),
    ])
    req = urllib.request.Request(
        COMFY_HOST + "/upload/image",
        data=body,
        method="POST",
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"}
    )
    with urllib.request.urlopen(req, timeout=60) as res:
        return json.loads(res.read().decode("utf-8"))


def download_comfy_output(filename, subfolder, out_filepath):
    """从 ComfyUI 下载输出文件"""
    params = urllib.parse.urlencode({"filename": filename, "subfolder": subfolder, "type": "output"})
    url = f"{COMFY_HOST}/view?{params}"
    with urllib.request.urlopen(url, timeout=120) as res, open(out_filepath, "wb") as f:
        f.write(res.read())
    return out_filepath


def run_comfy_prompt(graph, client_id="pet_pipeline", timeout=1200):
    """提交 Prompt Graph 并轮询等待执行完成"""
    res = api_post("/prompt", {"prompt": graph, "client_id": client_id})
    prompt_id = res["prompt_id"]
    print(f"  [ComfyUI] 任务已提交, ID: {prompt_id}, 等待推理中...")

    t0 = time.time()
    while time.time() - t0 < timeout:
        history = api_get(f"/history/{prompt_id}")
        if prompt_id in history:
            info = history[prompt_id]
            status = info.get("status", {})
            if status.get("status_str") == "error":
                raise RuntimeError(f"ComfyUI 执行报错: {json.dumps(status, ensure_ascii=False)}")
            outputs = info.get("outputs", {})
            if outputs or status.get("completed"):
                return outputs
        time.sleep(3)
    raise TimeoutError(f"ComfyUI 任务超时: {prompt_id}")


# =====================================================================
# 阶段 1: 角色卡片 Prompt 与 Graph 构建
# =====================================================================
def build_character_card_prompt(pet_name, pet_description, style="chibi mascot 3d render"):
    """
    遵循 qwen-image-character-sheet 工业规范生成角色全景参考表
    包含：三视图 (Front/Side/Back)、45度视角、表情特写与材质细节
    """
    prompt = f"""A master-quality, professional CHARACTER REFERENCE SHEET of {pet_name}, a {pet_description}.
Clean solid pure white background, flat bright studio lighting, 8k resolution, razor-sharp focus, uniform color grading.
Style: {style}, highly detailed model sheet layout, precise character consistency.

The image is arranged into a clean modular turnaround reference grid:
1. HEAD AND FACIAL DETAILS:
- Direct front facial portrait with natural cute expression
- 90-degree right profile headshot showing muzzle, ears and cheeks
- Happy expression close-up and dynamic perked ear/eye detail

2. FULL-BODY TURNAROUND:
Four orthographic standing figures of the exact same character aligned at identical scale and height:
- 01 Front View: full body facing directly forward, resting pose
- 02 Left 45-degree angle: full body dynamic three-quarter view
- 03 Side Profile: full body pure 90-degree side profile view
- 04 Back View: full body facing backward showing back contour and tail

3. MATERIAL AND ACCESSORY CLOSE-UPS:
- Extreme close-up of skin/fur texture, material sheen and subtle seam details
- Close-up of accessories, collar, eyes or mechanical markings

CHARACTER CONSISTENCY:
Preserve exact colors, markings, eye shapes, proportions, and adorable silhouette across all views.
Solid pure white background with subtle ground shadows. No scenery, no text captions, no speech bubbles."""
    return prompt


def create_character_card_graph(prompt, ref_image_comfy=None, seed=42):
    """构建角色卡片生成 Graph (16:9 画布)"""
    graph = {
        "2": {"class_type": "UNETLoader", "inputs": {"unet_name": "qwen_image_2.1_int8_convrot.safetensors", "weight_dtype": "default"}},
        "3": {"class_type": "QwenImage21Cache", "inputs": {"model": ["2", 0], "device": "auto", "dtype": "default"}},
        "4": {"class_type": "CLIPLoader", "inputs": {"clip_name": "qwen3vl_8b_w4a8.safetensors", "type": "qwen_image", "device": "default"}},
        "5": {"class_type": "VAELoader", "inputs": {"vae_name": "qwen_image_2.1_vae_bf16.safetensors"}},
    }

    if ref_image_comfy:
        graph["6"] = {"class_type": "LoadImage", "inputs": {"image": ref_image_comfy}}
        graph["8"] = {
            "class_type": "TextEncodeQwenImage21",
            "inputs": {
                "clip": ["4", 0],
                "images": [["6", 0]],
                "vae": ["5", 0],
                "prompt": prompt,
                "negative_prompt": NEGATIVE_PROMPT,
                "resolution": 1024
            }
        }
    else:
        graph["8"] = {
            "class_type": "TextEncodeQwenImage21",
            "inputs": {
                "clip": ["4", 0],
                "images": [],
                "vae": ["5", 0],
                "prompt": prompt,
                "negative_prompt": NEGATIVE_PROMPT,
                "resolution": 1024
            }
        }

    # 16:9 比例 (1280x768)
    graph["9"] = {"class_type": "EmptyLatentImage", "inputs": {"width": 1280, "height": 768, "batch_size": 1}}
    graph["11"] = {
        "class_type": "KSampler",
        "inputs": {
            "model": ["3", 0],
            "positive": ["8", 0],
            "negative": ["8", 1],
            "latent_image": ["9", 0],
            "seed": seed,
            "steps": 25,
            "cfg": 1.0,
            "sampler_name": "euler",
            "scheduler": "simple",
            "denoise": 1.0
        }
    }
    graph["12"] = {"class_type": "VAEDecode", "inputs": {"samples": ["11", 0], "vae": ["5", 0]}}
    graph["13"] = {"class_type": "SaveImage", "inputs": {"images": ["12", 0], "filename_prefix": "pet_character_card"}}
    return graph


# =====================================================================
# 阶段 2: 4×4 连贯 16 动作表 Prompt 与 Graph 构建
# =====================================================================
def build_16_pose_prompt(pet_name, ref_desc):
    """
    遵循 generate-16-pose-sheet 规范生成 16 格动作姿态表 Prompt
    """
    actions_formatted = "\n".join([f"{act}" for act in PET_16_ACTIONS])
    prompt = f"""Create a 4×4 action pose sheet featuring {pet_name} from the uploaded reference character sheet, with exactly 16 equally sized panels in one square image.

CHARACTER CONSISTENCY:
Use the uploaded character reference card as the sole visual anchor for identity and appearance.
Preserve the exact face, ears, fur/color palette, body proportions, markings, accessories, and artistic style across all 16 panels.
Do not redesign the character or alter outfit/color. Infer unshown angles strictly consistent with the reference.

ADAPTIVE CHOREOGRAPHY (16 SEQUENTIAL POSES):
Show 16 distinct, sequential, connected key poses for this cute character, ordered from left to right and top to bottom:
{actions_formatted}

COMPOSITION AND PRESENTATION:
- Format: Strict 4×4 grid of 16 equal square panels arranged in 4 rows and 4 columns.
- Background: Plain clean pure white background with thin black grid dividing lines.
- Numbering: Small, legible numbers 1 to 16 in the top-left corner of each corresponding panel.
- Framing: Exactly one full-body depiction of the character per panel. Fixed camera framing and consistent scale across all panels.
- Margins: 10%-15% comfortable safety margins around head, feet, and limbs so no parts are cropped.
- Physics: Subtle soft ground contact shadows.

AVOID:
Repeated poses, identity drift, face deformation, color shifting, missing limbs, cropped bodies outside grid, motion blur, scenic background, speech bubbles, watermarks.

Final output: One polished square image containing a clearly organized 4×4 grid of 16 distinct, expressive, full-body keyframe poses."""
    return prompt


def create_16_pose_graph(card_image_comfy, prompt, seed=101):
    """以角色卡片为单图参考，生成 1024x1024 4x4 动作表"""
    return {
        "2": {"class_type": "UNETLoader", "inputs": {"unet_name": "qwen_image_2.1_int8_convrot.safetensors", "weight_dtype": "default"}},
        "3": {"class_type": "QwenImage21Cache", "inputs": {"model": ["2", 0], "device": "auto", "dtype": "default"}},
        "4": {"class_type": "CLIPLoader", "inputs": {"clip_name": "qwen3vl_8b_w4a8.safetensors", "type": "qwen_image", "device": "default"}},
        "5": {"class_type": "VAELoader", "inputs": {"vae_name": "qwen_image_2.1_vae_bf16.safetensors"}},
        "6": {"class_type": "LoadImage", "inputs": {"image": card_image_comfy}},
        "8": {
            "class_type": "TextEncodeQwenImage21",
            "inputs": {
                "clip": ["4", 0],
                "images": [["6", 0]],
                "vae": ["5", 0],
                "prompt": prompt,
                "negative_prompt": NEGATIVE_PROMPT,
                "resolution": 1024
            }
        },
        "9": {"class_type": "EmptyLatentImage", "inputs": {"width": 1024, "height": 1024, "batch_size": 1}},
        "11": {
            "class_type": "KSampler",
            "inputs": {
                "model": ["3", 0],
                "positive": ["8", 0],
                "negative": ["8", 1],
                "latent_image": ["9", 0],
                "seed": seed,
                "steps": 25,
                "cfg": 1.0,
                "sampler_name": "euler",
                "scheduler": "simple",
                "denoise": 1.0
            }
        },
        "12": {"class_type": "VAEDecode", "inputs": {"samples": ["11", 0], "vae": ["5", 0]}},
        "13": {"class_type": "SaveImage", "inputs": {"images": ["12", 0], "filename_prefix": "pet_16_pose_sheet"}}
    }


# =====================================================================
# 阶段 3: 超分放大与切片封装
# =====================================================================
def create_upscale_graph(input_image_comfy, model_name=UPSCALE_MODEL_NAME):
    """构建超分放大 Graph (RealESRGAN_x2plus)"""
    return {
        "1": {"class_type": "LoadImage", "inputs": {"image": input_image_comfy}},
        "2": {"class_type": "UpscaleModelLoader", "inputs": {"model_name": model_name}},
        "3": {"class_type": "ImageUpscaleWithModel", "inputs": {"upscale_model": ["2", 0], "image": ["1", 0]}},
        "4": {"class_type": "SaveImage", "inputs": {"images": ["3", 0], "filename_prefix": "sr_16_pose_sheet"}}
    }


def slice_16_poses(image_path, output_dir, make_gif=True, duration_ms=200):
    """
    将 4x4 姿态表切分成 16 张独立关键帧，并合成预览动图
    """
    os.makedirs(output_dir, exist_ok=True)
    img = Image.open(image_path).convert("RGB")
    w, h = img.size
    col_w = w / 4.0
    row_h = h / 4.0

    sliced_frames = []
    print(f"  [切片处理] 开始裁切 4×4 网格: 尺寸 {w}×{h}...")

    for row in range(4):
        for col in range(4):
            idx = row * 4 + col + 1
            x1 = int(round(col * col_w))
            y1 = int(round(row * row_h))
            x2 = int(round((col + 1) * col_w))
            y2 = int(round((row + 1) * row_h))

            # 向内收缩 1.2% 边距，过滤掉黑色网格分隔线
            pad_x = int(round(col_w * 0.012))
            pad_y = int(round(row_h * 0.012))
            crop_box = (x1 + pad_x, y1 + pad_y, x2 - pad_x, y2 - pad_y)

            tile = img.crop(crop_box)
            out_file = os.path.join(output_dir, f"pose_{idx:02d}.png")
            tile.save(out_file)
            sliced_frames.append(tile)

    print(f"  [切片处理] 成功导出 16 张独立关键帧到: {output_dir}")

    if make_gif and sliced_frames:
        gif_path = os.path.join(output_dir, "action_preview.gif")
        webp_path = os.path.join(output_dir, "action_anim.webp")

        sliced_frames[0].save(
            gif_path,
            save_all=True,
            append_images=sliced_frames[1:],
            optimize=True,
            duration=duration_ms,
            loop=0
        )
        sliced_frames[0].save(
            webp_path,
            save_all=True,
            append_images=sliced_frames[1:],
            duration=duration_ms,
            loop=0
        )
        print(f"  [动图生成] 导出预览动图: {gif_path} 及 {webp_path}")

    return sliced_frames


# =====================================================================
# 流程编排主函数
# =====================================================================
def run_pet_pipeline(species, stage, ref_image=None, step="all", output_dir=None, seed=42):
    if output_dir is None:
        output_dir = os.path.join("design", "pose_pipeline_output", f"{species}_{stage}")
    os.makedirs(output_dir, exist_ok=True)

    print("=" * 70)
    print(f"🐾 启动宠物动作生产管线: 物种={species}, 阶段={stage}, 模式={step}")
    print(f"📁 产物目录: {output_dir}")
    print("=" * 70)

    # 1. 阶段 1: 角色卡片 (Character Sheet)
    card_path = os.path.join(output_dir, "character_card.png")
    if step in ("all", "card"):
        print("\n▶ [阶段 1/3] 生成/校验宠物角色卡片...")
        # 优先复用已有参考原画
        ref_name = None
        if ref_image and os.path.exists(ref_image):
            print(f"  使用传入参考原画: {ref_image}")
            up_res = upload_to_comfy(ref_image)
            ref_name = up_res["name"]
        else:
            default_ref = os.path.join("design", "assets_output", "pets", species, f"{stage}.png")
            if os.path.exists(default_ref):
                print(f"  使用项目库默认参考图: {default_ref}")
                up_res = upload_to_comfy(default_ref)
                ref_name = up_res["name"]

        card_prompt = build_character_card_prompt(
            pet_name=f"{species.capitalize()} Stage {stage}",
            pet_description=f"cute adorable chibi {species} mascot with signature proportions and expressive eyes"
        )
        card_graph = create_character_card_graph(card_prompt, ref_image_comfy=ref_name, seed=seed)
        outputs = run_comfy_prompt(card_graph, client_id="pet_card")
        
        # 提取生成的角色卡
        card_out = outputs["13"]["images"][0]
        download_comfy_output(card_out["filename"], card_out.get("subfolder", ""), card_path)
        print(f"  ✓ 阶段 1 完成: 角色卡片已保存至 {card_path}")

    if step == "card":
        print("\n🎉 单步任务已完成: 角色卡片已就绪。")
        return

    # 2. 阶段 2: 16 动作姿态表 (16-Pose Sheet)
    pose_sheet_path = os.path.join(output_dir, "16_pose_sheet_raw.png")
    if step in ("all", "poses"):
        print("\n▶ [阶段 2/3] 根据角色卡片生成 4×4 连贯 16 姿态表...")
        if not os.path.exists(card_path):
            raise FileNotFoundError(f"未找到前置角色卡片: {card_path}, 请先运行 step=card")

        up_card = upload_to_comfy(card_path)
        pose_prompt = build_16_pose_prompt(
            pet_name=f"{species.capitalize()} Stage {stage}",
            ref_desc="the exact chibi pet in the reference character sheet"
        )
        pose_graph = create_16_pose_graph(up_card["name"], pose_prompt, seed=seed + 10)
        outputs = run_comfy_prompt(pose_graph, client_id="pet_poses")

        pose_out = outputs["13"]["images"][0]
        download_comfy_output(pose_out["filename"], pose_out.get("subfolder", ""), pose_sheet_path)
        print(f"  ✓ 阶段 2 完成: 16 姿态大图已保存至 {pose_sheet_path}")

    if step == "poses":
        print("\n🎉 单步任务已完成: 16 姿态表已生成。")
        return

    # 3. 阶段 3: 超分放大与自动化切片 (Super-Resolution & Slicing)
    sr_pose_sheet_path = os.path.join(output_dir, "16_pose_sheet_sr.png")
    if step in ("all", "upscale"):
        print("\n▶ [阶段 3/3] 执行超分辨率放大 (RealESRGAN_x2plus) 并切片...")
        target_sheet = pose_sheet_path if os.path.exists(pose_sheet_path) else os.path.join(output_dir, "16_pose_sheet_raw.png")
        if not os.path.exists(target_sheet):
            raise FileNotFoundError(f"未找到待超分的姿态表: {target_sheet}")

        up_target = upload_to_comfy(target_sheet)
        sr_graph = create_upscale_graph(up_target["name"])
        outputs = run_comfy_prompt(sr_graph, client_id="pet_upscale")

        sr_out = outputs["4"]["images"][0]
        download_comfy_output(sr_out["filename"], sr_out.get("subfolder", ""), sr_pose_sheet_path)
        print(f"  ✓ 超分放大完成: 高清姿态图已保存至 {sr_pose_sheet_path}")

        # 切片提取 16 张独立关键帧与预览动图
        frames_dir = os.path.join(output_dir, "poses_512")
        slice_16_poses(sr_pose_sheet_path, frames_dir, make_gif=True, duration_ms=200)

    print("\n" + "=" * 70)
    print("✨ 全流程 3 阶段端到端资产生产圆满完成！")
    print(f"1. 角色卡片: {card_path}")
    print(f"2. 16动作表: {pose_sheet_path}")
    print(f"3. 超分大图: {sr_pose_sheet_path}")
    print(f"4. 独立关键帧(16张): {os.path.join(output_dir, 'poses_512')}")
    print("=" * 70)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="宠物资产端到端工业管线 (角色卡 -> 16动作 -> 超分切片)")
    parser.add_argument("--species", default="bunny", help="宠物物种 (bunny, chick, cat, dog...)")
    parser.add_argument("--stage", type=int, default=1, help="宠物成长阶段 (1, 2, 3)")
    parser.add_argument("--ref-image", default=None, help="自定义参考原画路径")
    parser.add_argument("--step", choices=["all", "card", "poses", "upscale"], default="all", help="执行阶段")
    parser.add_argument("--output-dir", default=None, help="产物输出目录")
    parser.add_argument("--seed", type=int, default=42, help="随机种子")
    args = parser.parse_args()

    run_pet_pipeline(
        species=args.species,
        stage=args.stage,
        ref_image=args.ref_image,
        step=args.step,
        output_dir=args.output_dir,
        seed=args.seed
    )
