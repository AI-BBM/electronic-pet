#!/usr/bin/env python3
"""Issue #4 素材接入工具链测试套件（T1–T22，对应测试契约，可复跑）。

运行：
    python tools/assets/tests/run_tests.py
全部通过退出码 0，任一失败退出码 1 并打印失败详情。

与最初一次性契约的两处差异（合并后固化为常态检查）：
- T2 原为「diff 相对基点 a63ca78 只增不删」，PR 合并后不可复现；
  固化为工作树卫生检查：git status 不得出现 tools/assets/ 之外的 M/D/R/A 条目，
  且六件交付物均已被 git 跟踪；
- T19 原在仓库根直接跑默认 --dst 并手工删除 assets/；固化为在临时目录内
  以子进程 chdir 运行，验证默认目标 assets/oss-mock/ 且不污染仓库。

T3/T4 为契约修正后的最终版（≤512 不放大；T4 用 1px 边界圈 fixture，
理由见 PR #5 描述）。T22 为 PM 定稿 canonical 12 物种表（species.meta.json）
入库后追加的锚点校验（8 common / 3 rare / 1 epic）。
"""
import json
import os
import py_compile
import subprocess
import sys
import tempfile
import traceback
from pathlib import Path

from PIL import Image

sys.stdout.reconfigure(encoding="utf-8")
sys.stderr.reconfigure(encoding="utf-8")

REPO = Path(__file__).resolve().parents[3]
ASSETS = REPO / "tools" / "assets"
SCRIPTS = ("process_assets", "gen_silhouette", "build_manifest", "upload_oss")
DELIVERABLES = (
    "process_assets.py",
    "gen_silhouette.py",
    "build_manifest.py",
    "upload_oss.py",
    "manifest.schema.json",
    "README.md",
)


def run(*args, cwd=None, env=None):
    return subprocess.run(
        [sys.executable, *[str(a) for a in args]],
        capture_output=True,
        text=True,
        cwd=str(cwd) if cwd else None,
        env=env,
    )


def tool(name):
    return ASSETS / f"{name}.py"


def tree_bytes(root):
    return {
        p.relative_to(root).as_posix(): p.read_bytes()
        for p in sorted(Path(root).rglob("*"))
        if p.is_file()
    }


def make_layout(root, species=("cat", "drake"), rarities=("common", "rare", "epic"),
                stages=("1.png", "2.png", "3.png", "silhouette.png"), size=8):
    L = Path(root)
    for sp in species:
        for f in stages:
            d = L / "pets" / sp
            d.mkdir(parents=True, exist_ok=True)
            Image.new("RGBA", (size, size), (1, 2, 3, 255)).save(d / f)
    for r in rarities:
        d = L / "eggs"
        d.mkdir(parents=True, exist_ok=True)
        Image.new("RGBA", (size, size), (4, 5, 6, 255)).save(d / f"{r}.png")
    return L


def write_meta(path, meta):
    Path(path).write_text(
        json.dumps(meta, ensure_ascii=False), encoding="utf-8"
    )


# ---------------------------------------------------------------- T1–T21


def test_t1(tmp):
    "交付物齐全、可编译、--help 可用（无 oss2 环境）"
    for name in DELIVERABLES:
        assert (ASSETS / name).is_file(), f"缺少交付物 {name}"
    pyc = tmp / "pyc"
    pyc.mkdir()
    for name in DELIVERABLES:
        if name.endswith(".py"):
            env = dict(os.environ, PYTHONPYCACHEPREFIX=str(pyc))
            r = run("-m", "py_compile", ASSETS / name, env=env)
            assert r.returncode == 0, f"py_compile {name}: {r.stderr}"
    for s in SCRIPTS:
        r = run(tool(s), "--help")
        assert r.returncode == 0, f"{s} --help: rc={r.returncode} {r.stderr}"
        assert r.stdout.strip(), f"{s} --help 无输出"


def test_t2(tmp):
    "工作树卫生：变更不出 tools/assets/，交付物均被 git 跟踪"
    r = subprocess.run(["git", "status", "--porcelain"], cwd=str(REPO),
                       capture_output=True, text=True)
    assert r.returncode == 0, r.stderr
    for line in r.stdout.splitlines():
        code, path = line[:2], line[3:]
        tracked_change = code.strip() in ("M", "D", "R", "A")
        if tracked_change and not path.startswith("tools/assets/"):
            raise AssertionError(f"tools/assets/ 之外的变更：{line}")
    tracked = subprocess.run(["git", "ls-files", "tools/assets"], cwd=str(REPO),
                             capture_output=True, text=True).stdout.split()
    for name in DELIVERABLES:
        assert f"tools/assets/{name}" in tracked, f"{name} 未入库"


def test_t3(tmp):
    "裁切=alpha 包围盒；>512 长边缩到 512；比例与透明保留；≤512 不放大"
    src, out = tmp / "in", tmp / "out"
    src.mkdir()
    im = Image.new("RGBA", (800, 600), (0, 0, 0, 0))
    im.paste(Image.new("RGBA", (200, 100), (255, 0, 0, 255)), (200, 100))
    im.save(src / "block.png")
    Image.new("RGBA", (1024, 768), (0, 128, 255, 255)).save(src / "full.png")
    r = run(tool("process_assets"), "--src", src, "--dst", out)
    assert r.returncode == 0, r.stderr
    assert sorted(p.name for p in out.iterdir()) == ["block.png", "full.png"]
    b = Image.open(out / "block.png")
    assert b.mode == "RGBA" and b.size == (200, 100), (b.mode, b.size)
    assert b.getchannel("A").getbbox() == (0, 0, 200, 100)
    assert b.getpixel((5, 5)) == (255, 0, 0, 255)
    f = Image.open(out / "full.png")
    assert f.mode == "RGBA" and f.size == (512, 384), (f.mode, f.size)
    assert f.getchannel("A").getbbox() == (0, 0, 512, 384)
    assert f.getpixel((10, 10))[3] == 255


def test_t4(tmp):
    "长边 510（≤512）不放大，且逐字节等于裁切输入"
    src, out = tmp / "in", tmp / "out"
    src.mkdir()
    im = Image.new("RGBA", (512, 300), (10, 20, 30, 255))
    px = im.load()
    for x in range(512):
        px[x, 0] = (10, 20, 30, 0)
        px[x, 299] = (10, 20, 30, 0)
    for y in range(300):
        px[0, y] = (10, 20, 30, 0)
        px[511, y] = (10, 20, 30, 0)
    im.save(src / "e512.png")
    assert im.getchannel("A").getbbox() == (1, 1, 511, 299)
    r = run(tool("process_assets"), "--src", src, "--dst", out)
    assert r.returncode == 0, r.stderr
    o = Image.open(out / "e512.png")
    assert o.size == (510, 298), o.size
    assert o.tobytes() == im.crop(im.getchannel("A").getbbox()).tobytes()


def test_t5(tmp):
    "小于 512 的输入不放大"
    src, out = tmp / "in", tmp / "out"
    src.mkdir()
    Image.new("RGBA", (300, 200), (9, 8, 7, 255)).save(src / "small.png")
    r = run(tool("process_assets"), "--src", src, "--dst", out)
    assert r.returncode == 0, r.stderr
    o = Image.open(out / "small.png")
    assert o.size == (300, 200), o.size
    assert o.getpixel((150, 100)) == (9, 8, 7, 255)


def test_t6(tmp):
    "无法解码 / 非 PNG 内容：非零退出并点名文件（含单独重跑）"
    src, out = tmp / "in", tmp / "out"
    src.mkdir()
    (src / "fake.png").write_text("this is not a png", encoding="utf-8")
    Image.new("RGB", (64, 64), (200, 100, 50)).save(src / "photo.jpg")
    r = run(tool("process_assets"), "--src", src, "--dst", out)
    assert r.returncode != 0, "坏输入必须整体非零退出"
    assert ("fake.png" in r.stderr) or ("photo.jpg" in r.stderr), r.stderr
    for name, marker in (("fake.png", "fake.png"), ("photo.jpg", "photo.jpg")):
        only = tmp / f"only_{name}"
        only.mkdir()
        (only / name).write_bytes((src / name).read_bytes())
        r1 = run(tool("process_assets"), "--src", only, "--dst", tmp / "o")
        assert r1.returncode != 0, f"{name} 单独存在时应非零退出"
        assert marker in r1.stderr, r1.stderr


def test_t7(tmp):
    "无 alpha 通道的 PNG：非零退出并说明原因"
    src, out = tmp / "in", tmp / "out"
    src.mkdir()
    Image.new("RGB", (64, 64), (200, 100, 50)).save(src / "noalpha.png")
    r = run(tool("process_assets"), "--src", src, "--dst", out)
    assert r.returncode != 0
    assert ("alpha" in r.stderr.lower()) or ("rgba" in r.stderr.lower()) or ("透明" in r.stderr)


def test_t8(tmp):
    "全透明 PNG（包围盒为空）：非零退出并说明原因"
    src, out = tmp / "in", tmp / "out"
    src.mkdir()
    Image.new("RGBA", (64, 64), (0, 0, 0, 0)).save(src / "blank.png")
    r = run(tool("process_assets"), "--src", src, "--dst", out)
    assert r.returncode != 0
    low = r.stderr.lower()
    assert any(k in low for k in ("transparent", "empty", "alpha")) or ("全透明" in r.stderr)


def test_t9(tmp):
    "剪影：尺寸/mode 不变，alpha 逐像素一致，可见像素纯黑（半透明 alpha 保留）"
    src, out = tmp / "in", tmp / "out"
    src.mkdir()
    out.mkdir()
    im = Image.new("RGBA", (120, 80), (255, 0, 0, 255))
    for y in range(80):
        for x in range(60, 120):
            im.putpixel((x, y), (0, 0, 255, 128))
    im.save(src / "sil_in.png")
    r = run(tool("gen_silhouette"), "--src", src / "sil_in.png", "--dst", out)
    assert r.returncode == 0, r.stderr
    s = Image.open(out / "sil_in.png")
    assert s.mode == "RGBA" and s.size == im.size
    assert s.getchannel("A").tobytes() == im.getchannel("A").tobytes()
    ps = s.load()
    for y in range(im.height):
        for x in range(im.width):
            rr, gg, bb, al = ps[x, y]
            if al > 0:
                assert (rr, gg, bb) == (0, 0, 0), ((x, y), ps[x, y])
    assert ps[10, 10] == (0, 0, 0, 255)
    assert ps[90, 40] == (0, 0, 0, 128)


def test_t10(tmp):
    "剪影目录模式批处理且幂等"
    src = tmp / "in"
    src.mkdir()
    Image.new("RGBA", (60, 60), (255, 0, 0, 255)).save(src / "a.png")
    im = Image.new("RGBA", (50, 40), (0, 0, 0, 0))
    im.paste(Image.new("RGBA", (10, 10), (0, 255, 0, 200)), (5, 5))
    im.save(src / "b.png")
    o1, o2 = tmp / "o1", tmp / "o2"
    for dst in (o1, o2):
        r = run(tool("gen_silhouette"), "--src", src, "--dst", dst)
        assert r.returncode == 0, r.stderr
    assert tree_bytes(o1) == tree_bytes(o2)
    assert set(tree_bytes(o1)) == {"a.png", "b.png"}
    r = run(tool("gen_silhouette"), "--src", src, "--dst", o1)
    assert r.returncode == 0, r.stderr
    assert tree_bytes(o1) == tree_bytes(o2)


def test_t11(tmp):
    "剪影坏输入（无 alpha / 伪文件）：目录与单文件模式均非零退出并点名"
    src, out = tmp / "in", tmp / "out"
    src.mkdir()
    out.mkdir()
    Image.new("RGB", (30, 30), (1, 2, 3)).save(src / "rgb.png")
    (src / "fake.png").write_text("junk", encoding="utf-8")
    r = run(tool("gen_silhouette"), "--src", src, "--dst", out)
    assert r.returncode != 0
    assert ("rgb.png" in r.stderr) or ("fake.png" in r.stderr), r.stderr
    r1 = run(tool("gen_silhouette"), "--src", src / "rgb.png", "--dst", out)
    assert r1.returncode != 0


def test_t12(tmp):
    "manifest 正常路径：结构、相对键、中文名、schema 键齐备"
    L = make_layout(tmp / "layout")
    meta = tmp / "meta.json"
    write_meta(meta, {"cat": {"name": "星芒猫", "rarity": "common"},
                      "drake": {"name": "凛光龙", "rarity": "epic"}})
    dst = L / "manifest.json"
    r = run(tool("build_manifest"), "--src", L, "--meta", meta, "--dst", dst)
    assert r.returncode == 0, r.stderr
    m = json.loads(dst.read_text(encoding="utf-8"))
    assert set(m) >= {"version", "eggs", "species"}
    assert isinstance(m["version"], str) and m["version"]
    assert set(m["eggs"]) == {"common", "rare", "epic"}
    for rr in ("common", "rare", "epic"):
        assert m["eggs"][rr].endswith(f"eggs/{rr}.png")
    assert len(m["species"]) == 2
    for sp in m["species"]:
        assert set(sp) >= {"id", "name", "rarity", "stages", "silhouette"}
        assert sp["rarity"] in {"common", "rare", "epic"}
        assert set(sp["stages"]) == {"1", "2", "3"}
        for k in "123":
            assert sp["stages"][k].endswith(f"pets/{sp['id']}/{k}.png")
        assert sp["silhouette"].endswith(f"pets/{sp['id']}/silhouette.png")
    byid = {s["id"]: s for s in m["species"]}
    assert byid["cat"]["name"] == "星芒猫" and byid["drake"]["name"] == "凛光龙"
    schema = json.loads((ASSETS / "manifest.schema.json").read_text(encoding="utf-8"))
    assert "properties" in schema
    for k in ("version", "eggs", "species"):
        assert k in schema["properties"], k


def test_t13(tmp):
    "缺阶段图 2.png：非零退出、点名种类、不产出 manifest"
    L = make_layout(tmp / "layout", stages=("1.png", "3.png", "silhouette.png"))
    meta = tmp / "meta.json"
    write_meta(meta, {"cat": {"name": "A", "rarity": "common"},
                      "drake": {"name": "D", "rarity": "epic"}})
    dst = L / "manifest.json"
    r = run(tool("build_manifest"), "--src", L, "--meta", meta, "--dst", dst)
    assert r.returncode != 0
    assert "2.png" in r.stderr and "cat" in r.stderr, r.stderr
    assert not dst.exists()


def test_t14(tmp):
    "缺 silhouette.png：非零退出"
    L = make_layout(tmp / "layout", stages=("1.png", "2.png", "3.png"))
    meta = tmp / "meta.json"
    write_meta(meta, {"cat": {"name": "A", "rarity": "common"}})
    r = run(tool("build_manifest"), "--src", L, "--meta", meta, "--dst", L / "m.json")
    assert r.returncode != 0
    assert "silhouette" in r.stderr.lower(), r.stderr


def test_t15(tmp):
    "缺 eggs/rare.png：非零退出并可定位"
    L = make_layout(tmp / "layout", rarities=("common", "epic"))
    meta = tmp / "meta.json"
    write_meta(meta, {"cat": {"name": "A", "rarity": "common"}})
    r = run(tool("build_manifest"), "--src", L, "--meta", meta, "--dst", L / "m.json")
    assert r.returncode != 0
    low = r.stderr.lower()
    assert "rare" in low and "egg" in low, r.stderr


def test_t16(tmp):
    "稀有度非法（legendary）：非零退出并提示合法取值"
    L = make_layout(tmp / "layout")
    meta = tmp / "meta.json"
    write_meta(meta, {"cat": {"name": "A", "rarity": "legendary"}})
    r = run(tool("build_manifest"), "--src", L, "--meta", meta, "--dst", L / "m.json")
    assert r.returncode != 0
    low = r.stderr.lower()
    assert "legendary" in low and ("rarity" in low or "common" in low), r.stderr


def test_t17(tmp):
    "mock 上传：与源树字节级一致、幂等"
    L = make_layout(tmp / "layout", size=16)
    (L / "manifest.json").write_text(json.dumps(
        {"version": "1",
         "eggs": {r: f"eggs/{r}.png" for r in ("common", "rare", "epic")},
         "species": [{"id": "cat", "name": "A", "rarity": "common",
                      "stages": {k: f"pets/cat/{k}.png" for k in "123"},
                      "silhouette": "pets/cat/silhouette.png"}]}),
        encoding="utf-8")
    oss = tmp / "oss"
    for _ in range(2):
        r = run(tool("upload_oss"), "--src", L, "--mock", "--dst", oss)
        assert r.returncode == 0, r.stderr
        assert tree_bytes(L) == tree_bytes(oss), "mock 必须是纯字节拷贝"


def test_t18(tmp):
    "无凭据真实上传：非零退出、可读提示、无半成品"
    L = make_layout(tmp / "layout")
    (L / "manifest.json").write_text('{"version": "1"}', encoding="utf-8")
    env = {k: v for k, v in os.environ.items() if not k.startswith("OSS_")}
    never = tmp / "never"
    r = run(tool("upload_oss"), "--src", L, "--dst", never, env=env)
    assert r.returncode != 0
    low = r.stderr.lower()
    assert any(k in low for k in ("credential", "access", "oss_")) or "凭据" in r.stderr
    assert not never.exists(), "失败时不得创建目标目录"


def test_t19(tmp):
    "mock 默认目标 assets/oss-mock/（相对 CWD），不污染仓库"
    L = make_layout(tmp / "layout")
    (L / "manifest.json").write_text('{"version": "1"}', encoding="utf-8")
    cwd = tmp / "cwd"
    cwd.mkdir()
    r = run(tool("upload_oss"), "--src", L, "--mock", cwd=cwd)
    assert r.returncode == 0, r.stderr
    d = cwd / "assets" / "oss-mock"
    assert (d / "manifest.json").is_file()
    assert (d / "pets" / "cat" / "1.png").is_file()
    assert (d / "eggs" / "epic.png").is_file()
    assert not (REPO / "assets").exists(), "仓库内不得出现 assets/ 残留"


def test_t20(tmp):
    "process_assets 确定性 + 覆盖写幂等"
    src = tmp / "in"
    src.mkdir()
    im = Image.new("RGBA", (800, 600), (0, 0, 0, 0))
    im.paste(Image.new("RGBA", (200, 100), (255, 0, 0, 255)), (200, 100))
    im.save(src / "block.png")
    Image.new("RGBA", (1024, 768), (0, 128, 255, 255)).save(src / "full.png")
    oA, oB = tmp / "oA", tmp / "oB"
    for dst in (oA, oB):
        r = run(tool("process_assets"), "--src", src, "--dst", dst)
        assert r.returncode == 0, r.stderr
    assert tree_bytes(oA) == tree_bytes(oB)
    r = run(tool("process_assets"), "--src", src, "--dst", oA)
    assert r.returncode == 0, r.stderr
    assert tree_bytes(oA) == tree_bytes(oB)


def test_t21(tmp):
    "build_manifest 确定性 + 覆盖写幂等"
    L = make_layout(tmp / "layout")
    meta = tmp / "meta.json"
    write_meta(meta, {"cat": {"name": "星芒猫", "rarity": "common"},
                      "drake": {"name": "凛光龙", "rarity": "epic"}})
    m1, m2 = tmp / "m1.json", tmp / "m2.json"
    for dst in (m1, m2):
        r = run(tool("build_manifest"), "--src", L, "--meta", meta, "--dst", dst)
        assert r.returncode == 0, r.stderr
    assert m1.read_bytes() == m2.read_bytes()
    r = run(tool("build_manifest"), "--src", L, "--meta", meta, "--dst", m1)
    assert r.returncode == 0, r.stderr
    assert m1.read_bytes() == m2.read_bytes()


CANONICAL_IDS = (
    "cat", "dog", "bunny", "hamster", "chick", "penguin",
    "koala", "axolotl", "fox", "panda", "dino", "dragon",
)


def test_t22(tmp):
    "species.meta.json（canonical 12 物种表）：id 齐全、稀有度合法且分布 8/3/1"
    path = ASSETS / "species.meta.json"
    assert path.is_file(), "缺少 canonical 物种表 tools/assets/species.meta.json"
    meta = json.loads(path.read_text(encoding="utf-8"))
    assert isinstance(meta, dict), "species.meta.json 顶层必须是对象"
    assert sorted(meta) == sorted(CANONICAL_IDS), sorted(meta)
    dist = {"common": 0, "rare": 0, "epic": 0}
    for sp_id, info in meta.items():
        assert isinstance(info, dict), sp_id
        assert isinstance(info.get("name"), str) and info["name"], sp_id
        rarity = info.get("rarity")
        assert rarity in dist, (sp_id, rarity)
        dist[rarity] += 1
    assert dist == {"common": 8, "rare": 3, "epic": 1}, dist


TESTS = [
    test_t1, test_t2, test_t3, test_t4, test_t5, test_t6, test_t7,
    test_t8, test_t9, test_t10, test_t11, test_t12, test_t13, test_t14,
    test_t15, test_t16, test_t17, test_t18, test_t19, test_t20, test_t21,
    test_t22,
]


def main():
    failures = []
    with tempfile.TemporaryDirectory(prefix="assets-tests-") as td:
        tmp = Path(td)
        for fn in TESTS:
            case_tmp = tmp / fn.__name__
            case_tmp.mkdir()
            try:
                fn(case_tmp)
                print(f"{fn.__name__} PASS")
            except Exception:
                failures.append(fn.__name__)
                print(f"{fn.__name__} FAIL")
                traceback.print_exc()
    print(f"--- {len(TESTS) - len(failures)}/{len(TESTS)} passed ---")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
