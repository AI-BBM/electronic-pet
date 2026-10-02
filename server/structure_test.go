package server_test

// T1 结构完整性：M2 契约要求的实现/页面文件必须存在。
// 说明：鉴权实现由 M1 的 server/auth.go 提供，M2 不再自带 auth 文件。
// 配套 shell 用例（见测试报告）：实现落地后 go build ./... 与 go vet ./... 必须通过。
// 命令: go test ./server/ -run TestStructure_ImplementationFilesExist -v

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStructure_ImplementationFilesExist(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 无法定位测试文件所在目录")
	}
	root := filepath.Dir(filepath.Dir(thisFile)) // server/ 的上一级即仓库根
	for _, name := range []string{
		"server/points.go",
		"server/levels.go",
		"web/log.html",
		"web/static/js/points.js",
		"web/static/css/points.css",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Errorf("缺少实现文件 %s: %v", name, err)
		}
	}
}
