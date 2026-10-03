# 部署手册 · pet.aibbm.cn

> 面板优先原则与同机禁碰清单同 school_habit（见 baota-deploy skill），本文只记本项目差异与实战教训。

## 拓扑

| 层 | 值 |
|---|---|
| 站点 | 宝塔 HTML 项目 `pet.aibbm.cn`（静态+反代），根目录 /www/wwwroot/pet.aibbm.cn（占位） |
| 反代 | `location ^~ / → http://127.0.0.1:18082`，发送域名 `$host` |
| SSL | Let's Encrypt 文件验证，强制 HTTPS 开启 |
| 进程 | `/www/wwwroot/electronicpet/pet`，用户 `www`，监听 **127.0.0.1:18082**（回环，防绕过反代直连公网） |
| 配置 | `/www/wwwroot/electronicpet/.env`（export 格式，600，www 属主）：`PET_ADDR="127.0.0.1:18082"`、`PET_DB="data/pet.db"` |
| 数据 | `/www/wwwroot/electronicpet/data/`（SQLite WAL），发版绝不删改 |
| 兜底 | root crontab `@reboot sleep 40` 拉起（pgrep 查重） |

## 发版六步（Windows 开发机 Git Bash）

```bash
# 1. 构建（纯 Go SQLite，无需 CGO；前端内嵌无需 npm）
cd D:/hhhuaang/electronic-pet/electronic-pet
git pull origin main && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o pet_linux ./cmd/pet/...
# 2. 预检
ssh root@112.124.59.165 'ss -lntp | grep 18082; curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:18082/'
# 3. 备份（二进制 + 数据）
ssh root@112.124.59.165 'cd /www/wwwroot/electronicpet && cp pet pet.bak && tar czf data_backup_$(date +%F_%H%M).tar.gz data/'
# 4. 上传切换
scp pet_linux root@112.124.59.165:/www/wwwroot/electronicpet/pet.new
# 5. 切换+重启（必须 source ./.env；nohup 会让 ssh 句柄滞留，本地加 run_in_background）
ssh -n root@112.124.59.165 'cd /www/wwwroot/electronicpet && chmod +x pet.new && mv pet.new pet && chown www:www pet && pkill -f "^\./pet$"; sleep 1; sudo -u www bash -c "cd /www/wwwroot/electronicpet && source ./.env && nohup ./pet >> run.log 2>&1 &"; sleep 2; ss -lntp | grep 18082; tail -1 run.log; exit 0'
# 6. 验证（本机 + 邻居）
curl -s -o /dev/null -w "%{http_code}\n" https://pet.aibbm.cn/
curl -s -X POST https://pet.aibbm.cn/api/join -H "Content-Type: application/json" -d '{"classCode":"demo2026","name":"t","studentNo":"T999"}'
curl -s -o /dev/null -w "%{http_code}\n" https://edu.aibbm.cn/
```

## 回滚（一键）

```bash
ssh root@112.124.59.165 'cd /www/wwwroot/electronicpet && mv pet pet.bad && mv pet.bak pet && chown www:www pet && pkill -f "^\./pet$"; sleep 1; sudo -u www bash -c "cd /www/wwwroot/electronicpet && source ./.env && nohup ./pet >> run.log 2>&1 &"'
```

## ⚠️ 实战教训

1. **迁移必须兼容老库**（2026-10-03 事故）：`CREATE TABLE IF NOT EXISTS` 对已存在的老表是空操作，
   后续索引引用新列会直接 fatal（issue #11）。任何 schema 变更都要走
   「PRAGMA table_info 查列 → ALTER TABLE ADD COLUMN → 再建索引」路径，
   并配「旧 DDL 建库 → migrate 成功」回归测试（见 server/migrate_hotfix_test.go）。
   发版后第一件事看 `tail -1 run.log` 是否有 init server 报错。
2. **远程 nohup 必 `run_in_background` + 末尾 `exit 0`**，否则 ssh 句柄滞留拖住本地终端。
3. **回环监听**：PET_ADDR 必须写 `127.0.0.1:18082` 而非 `:18082`，避免绕过反代/SSL 直连公网。
4. **宝塔面板访问**：8888 直连 IP 或错误 UA 会被伪装成 nginx 404；用浏览器 UA 访问
   `http://112.124.59.165:8888/login`（面板账号见 baota-deploy skill，勿外传）。
5. 发版节奏遵循「合并即部署」（用户 2026-09-11 定调），但 **schema 变更类发版先在等价老库上演练 migrate**。
