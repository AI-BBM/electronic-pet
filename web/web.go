// Package web 内嵌前端静态资源。
package web

import "embed"

//go:embed all:static
var Static embed.FS
