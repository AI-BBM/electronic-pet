# 运维 SOP · 教师账号密码重置

> 适用：教师忘记密码且无自助改密能力时（#53）。生产库 `/www/wwwroot/electronicpet/data/pet.db`。
> 纪律：**备份先行 → 重置 → 验证 → 群内知会临时密码 → 提醒教师登录后立即改密**。

## 前置

- 服务器：112.124.59.165（宝塔），服务用户 `www`，库文件 `data/pet.db`（WAL）。
- 教师密码为 bcrypt 哈希存于 `teachers.pass_hash`（M6 邮箱账号体系）。

## 标准流程（五步）

```bash
# 1. 备份（数据库三件套原样留存）
ssh root@112.124.59.165 'cd /www/wwwroot/electronicpet && tar czf data_backup_reset_$(date +%F_%H%M).tar.gz data/ && echo BACKUP-OK'

# 2. 生成新临时密码的 bcrypt 哈希（本机 Go 一行程序，临时密码形如 Reset+6位随机）
#    本机执行，输出 <hash>：
go run - <<'EOF'
package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	max := big.NewInt(1000000)
	n, _ := rand.Int(rand.Reader, max)
	tmp := fmt.Sprintf("Reset%06d", n.Int64())
	h, err := bcrypt.GenerateFromPassword([]byte(tmp), bcrypt.DefaultCost)
	if err != nil {
		os.Exit(1)
	}
	fmt.Printf("临时密码: %s\nHASH: %s\n", tmp, h)
}
EOF

# 3. 重置（<email> 换目标教师邮箱，<hash> 换上一步输出；同时 pass_ver+1 使旧会话全部失效）
#    先查 id 确认目标唯一：
ssh root@112.124.59.165 "cd /www/wwwroot/electronicpet && sudo -u www sqlite3 data/pet.db \"SELECT id, class_id, email FROM teachers WHERE email='<email>';\""
#    再更新（<id>/<hash> 替换）：
ssh root@112.124.59.165 "cd /www/wwwroot/electronicpet && sudo -u www sqlite3 data/pet.db \"UPDATE teachers SET pass_hash='<hash>', pass_ver=pass_ver+1 WHERE id=<id>; SELECT changes();\""

# 4. 验证：教师用 临时密码 登录 pet.aibbm.cn → 200；旧密码登录 → 401
#    （curl 实测或请教师本人在页面操作）

# 5. 群内知会教师临时密码（私发更佳），并提醒：登录后在工作台「修改密码」
#    立即改成自己的密码（改密后旧会话自动失效）。
```

## 注意

- `sqlite3` 若服务器未装，可临时 `apt install -y sqlite3` 或用本机生成完整 UPDATE SQL 后由服务端执行。
- **严禁**把明文/临时密码写入任何仓库文件或 issue；群内知会后建议撤回消息。
- pass_ver 递增即旧 token 全失效，无需重启服务。
- 若教师邮箱也遗失（收不到验证码），走本 SOP 直接重置；邮箱遗失需同步更新 `teachers.email`（同语句改 email 列）。
