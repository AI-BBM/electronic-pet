// Command pet 电子宠物服务入口：Go 单二进制，SQLite + 前端内嵌。
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/AI-BBM/electronic-pet/server"
)

func main() {
	addr := envOr("PET_ADDR", ":8080")
	dbPath := envOr("PET_DB", "pet.db")

	handler, err := server.New(dbPath)
	if err != nil {
		log.Fatalf("init server: %v", err)
	}
	log.Printf("electronic-pet listening on %s (db=%s)", addr, dbPath)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
