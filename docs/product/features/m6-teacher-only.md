# 特性 PRD · M6 纯教师侧改造（班主任注册 / 名单管理 / 垃圾桶）

## 背景
黄总 2026-10-03 指示（19:19–19:21 三条）：产品改为**纯教师侧工具**——学生不登录、不注册、不进班；班主任注册登录后全权管理本班学生名单、发宠物、加分。删除学生进垃圾桶保留 3 个月可恢复。

## 产品形态变更
- **下线学生自助**：进入页/领蛋页/孵化过场/我的宠物等学生视图与端点（/api/join、/api/adopt、/api/points、/api/pet/me、/api/pet/me/log、/api/pet/name、/api/eggs）全部移除；存量学生 token 自然失效。
- **保留教师侧**（M4 端点延续，对象为名单内学生）：代发宠物、代加分（1–50，operator=teacher）、花名册。
- 数据（班级/学生/宠物/流水）完整保留，仅入口与操作者变更。

## 功能
### 1. 班主任注册与登录
- 注册：邮箱 + 密码（≥8 位）+ 班级名称 + 邮箱验证码（6 位、10 分钟有效、同邮箱 60s 限速、每日上限）；注册即建班，一生一班。
- 登录：邮箱 + 密码 → 教师 token（沿用 HMAC role=teacher，id=classID）。
- SMTP 发信（验证码邮件）：凭据走服务器 .env（SMTP_HOST/PORT/USER/PASS/FROM），不入仓库；开发期用 mock 发信（验证码记日志），未配置 SMTP 时注册接口返回明确提示。
- 存量班级：不自动迁移教师账号；管理员收集班主任邮箱后用管理脚本开通（脚本 tools/ 或一次性接口，二选一由实现定）。

### 2. 名单管理（工作台）
- 增：姓名 + 学号（班内唯一）；同班同学号重复 → 明确报错。
- 改：姓名、学号（学号改后班内仍须唯一）。
- 删：进垃圾桶（软删除），宠物与流水随学生保留、不再出现在花名册。
- 垃圾桶：列出已删学生（含删除时间）；**3 个月内可一键恢复**（学号未被在册学生占用即可恢复，冲突则提示）；超过 3 个月的记录由清理任务硬删除（学生+宠物+流水连带，启动时与每日定时执行）。
- 改名/命名：教师可随时改宠物名（移除 M1 的一次性限制）。

### 3. 发宠物 / 加分（沿用 M4）
- 代发宠物：随机种类、已领 409；代加分：1–50、理由必填、operator=teacher、requestId 幂等、跨级即时。
- 宠物展示（工作台行内/详情）与进化阶段沿用 species manifest。

## 页面（教师工作台，延续 M5 移动端适配）
- 注册页（邮箱/验证码/密码/班级名称）、登录页（邮箱/密码）。
- 工作台：在册名单表格（行内：发宠物/加分/改名/删除）+ 垃圾桶页签（恢复/彻底删除提示）。
- 移动端不回退（M5 断点体系延续）。

## 数据与迁移（⚠️ 走 #11 教训流程）
- 新表 teachers(id, class_id UNIQUE, email UNIQUE, pass_hash, created_at)。
- students 表**重建迁移**（SQLite 无法原地改 UNIQUE 约束）：新增 deleted_at DATETIME，UNIQUE(class_id, student_no) 改为部分唯一索引（WHERE deleted_at IS NULL）；必须配「M5 老库 → migrate 成功 + 数据无损 + 老流程回归」测试，含「垃圾桶恢复后学号可重新唯一」用例。
- 清理任务：启动时 + 每日，删除 deleted_at 超 3 个月的学生及其宠物、流水（事务内，先 pet 后 student 满足外键）。
- meta 新增注册验证码存储（或内存 + 单实例约束，实现定，测试钉死行为）。

## API 草案
- POST /api/teacher/register {email, passcode(验证码), password, className} → {token}
- POST /api/teacher/email-code {email} → 200（发码）
- POST /api/teacher/login {email, password} → {token}（替代 M4 口令登录；/api/teacher/login 旧口令式下线）
- POST /api/teacher/students {name, studentNo} → {student}
- PATCH /api/teacher/students/{id} {name?, studentNo?} → {student}
- DELETE /api/teacher/students/{id} → 200（入垃圾桶）
- GET /api/teacher/trash → 垃圾桶列表
- POST /api/teacher/students/{id}/restore → {student}
- POST /api/teacher/pets/{studentID}/name {name} → {pet}（教师改宠物名，替代 /api/pet/name）
- 沿用：POST /api/teacher/adopt、/api/teacher/points、GET /api/teacher/roster（含 adopted 与垃圾桶外过滤）

## 非目标
- 多教师共管、教师找回密码（邮箱重置留下一版）、学生侧任何形态、批量导入名单（后续可加 CSV）。

## 验收标准
- 邮箱注册全流程：发码（限速/过期/错误码）→ 注册成功建班登录；SMTP 未配置时给出明确错误。
- 名单 CRUD：增改删恢复全链路；垃圾桶 3 个月规则（清理任务可注入时钟测试）；恢复时学号冲突有明确提示。
- 代发/代加分回归（M4 用例全绿，仅入口变更）。
- 学生端点全部 404；学生 token 打任何端点 401/403。
- M5 老库迁移无损（回归测试覆盖，含垃圾桶恢复唯一性用例）。
- 桌面/移动端（M5 断点）不回退。
