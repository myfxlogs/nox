package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler 返回静态文件处理器：webDir 非空 = 服务该目录（dev 模式）；
// 空 = 服务嵌入的 dist 构建物（生产路径）。
// 缓存策略：index.html → no-cache（每次验证，hash 化 CSS/JS 引用变更即生效）；
// assets/* → max-age=31536000 immutable（hash 化文件名，内容变=文件名变）。
func Handler(webDir string) http.Handler {
	var h http.Handler
	if webDir != "" {
		h = http.FileServer(http.Dir(webDir))
	} else {
		sub, err := fs.Sub(dist, "dist")
		if err != nil {
			panic("web: embedded dist unavailable: " + err.Error())
		}
		h = http.FileServer(http.FS(sub))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/index.html") || r.URL.Path == "/" || r.URL.Path == "" {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		} else if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		h.ServeHTTP(w, r)
	})
}
