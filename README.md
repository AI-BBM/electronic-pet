# electronic-pet · 电子宠物班级激励网页

把班级日常激励变成看得见的成长：学生领蛋 → 孵化专属电子宠物 → 加分喂养 → 宠物升级进化。

- 产品文档：[docs/product/product.md](docs/product/product.md)（产品层）｜[domains.md](docs/product/domains.md)（域层）｜[features/](docs/product/features/)（特性层）｜[assets-spec.md](docs/product/assets-spec.md)（素材规范）
- 形态：PC 端网页，部署于 https://pet.aibbm.cn
- 素材：宠物立绘为透明背景 PNG，托管 OSS；由本机 ComfyUI（Qwen-Image 2.1 + BiRefNet 抠图）生成，探索稿见 [design/characters/](design/characters/)

## 技术约定
- 后端：Go 单二进制、SQLite、前端静态资源内嵌（与 school_habit 同部署模式，宝塔 112.124.59.165）
- 前端：原生/轻框架静态页，1280px+ 优先
- 图片：OSS + manifest.json 索引（见 assets-spec）

## 里程碑
| 里程碑 | 范围 | Issue |
|---|---|---|
| M1 领蛋与孵化 | 班级进入、领蛋、破壳、宠物档案 | #1 |
| M2 加分与升级 | 加分流水、等级阈值、进化表现 | #2 |
| M3 图鉴与班级墙 | 图鉴、排行、公开档案 | #3 |
| 素材接入 | species manifest、OSS 上传、剪影生成 | #4 |
