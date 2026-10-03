# 特性 PRD · M4 教师端

## 用户故事
老师以教师身份登录本班，代替/协助学生管理宠物：给学生发宠物（领蛋孵化）、给学生加分，并查看全班宠物状态。

## 身份与登录
- 每个班级绑定一个**教师密码**（teacher passcode）：
  - 班级首次被创建时系统生成，教师可修改（M4 提供修改接口）；
  - 登录方式：班级码 + 教师密码 → 签发教师 token（role=teacher，复用现有 HMAC token，payload 增加角色位）；
- 教师 token 与学生 token 权限严格隔离：教师不可代替学生“以学生身份”操作自己未授权的事务；学生 token 不可调用教师端点。

## 功能
1. **给学生发宠物**：
   - 输入学号（或花名册选择）→ 服务端代该学生走 adopt 流程（随机定种类，规则与 M1 一致）；
   - 学生已领养则返回冲突提示；
   - 发放后学生下次以自己身份进入即可看到宠物。
2. **给学生加分**：
   - 目标：班内任意已有宠物的学生；
   - 分值 1–50（教师额度高于学生自助的 1–10），理由必填（预设标签+自定义）；
   - 写入同一 point_logs（流水标记 operator=teacher，便于 M3 班级墙/审计展示）；
   - 跨级进化表现与学生端一致。
3. **班级花名册视图（只读）**：
   - 全班学生列表：姓名/学号、宠物种类/阶段、积分、最近加分时间；
   - 未领养学生明确标识（方便老师去发宠物）。

## API 草案
- POST /api/teacher/login {classCode, teacherPasscode} → {token}
- POST /api/teacher/adopt {studentNo} → {pet}（代领）
- POST /api/teacher/points {studentNo, reason, value, requestId} → {pet, levelUp, level}
- GET /api/teacher/roster → 全班花名册
- POST /api/teacher/passcode {oldPasscode, newPasscode} → 修改教师密码

## 数据与迁移（⚠️ 涉及 schema 变更，走 #11 教训流程）
- classes 表新增 teacher_passcode（迁移路径：pragma 查列 → ALTER ADD，默认置随机值）；
- point_logs 新增 operator TEXT DEFAULT 'student'（同上迁移路径 + 老库回归测试）；
- 迁移回归测试必须包含「M2 schema 老库 → migrate 成功」用例。

## 页面（PC 端）
- 教师登录页（班级码+教师密码）
- 教师工作台：花名册表格 + 行内「发宠物」「加分」操作 + 操作结果 toast

## 非目标
- 多教师共管一班、教师账号体系（用户名/密码找回）——后续里程碑；
- 教师端移动适配。

## 验收标准
- 学生 token 调教师端点一律 403；
- 教师代领：未领养学生成功发放、已领养返回 409；
- 教师加分 1–50 生效并写入流水（operator=teacher），跨级正确；
- 花名册数据与实际一致；
- M2 老库升级迁移无损（回归测试覆盖）。
