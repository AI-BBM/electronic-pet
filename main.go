// main 是 M2 骨架期临时入口（M1 合并 main 后以 M1 的 main.go 为准删除）。
// 用法：
//
//	electronic-pet -db data/pet.db -addr :8080
//	PET_LEVELS_FILE=levels.json electronic-pet   # 可选：自定义等级阈值 {"lv2":20,"lv3":60}
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	server "github.com/AI-BBM/electronic-pet/server"
)

func main() {
	dbPath := flag.String("db", "data/pet.db", "SQLite 数据库文件路径")
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	flag.Parse()

	if path := os.Getenv(server.LevelsFileEnv); path != "" {
		log.Printf("等级阈值文件: %s", path)
	} else {
		log.Printf("等级阈值: 默认 Lv2=20 Lv3=60（可用 %s 指定 JSON 文件）", server.LevelsFileEnv)
	}

	handler, err := server.New(*dbPath)
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}
	log.Printf("electronic-pet (M2 骨架) 监听 %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, handler))
}
