package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:web_dist
var webDist embed.FS

// registerConsole 挂载控制台静态资源伺服（/console/*）+ SPA fallback。
// 仅 admin.enabled=true 时挂载（与认证端点同开关，维持最小暴露面）。
//
// SPA fallback：命中真实静态文件则直出；否则（前端路由路径，如 /console/users）
// 一律回 index.html，交给前端 react-router 处理。控制台本身不做鉴权
// （标准 SPA 做法），安全边界在后端 API 层。
func (s *Server) registerConsole() {
	if !s.cfg.Admin.Enabled {
		return
	}
	// 剥掉 web_dist 前缀，让文件系统根对齐 /console/。
	sub, err := fs.Sub(webDist, "web_dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(sub))

	handler := func(c *gin.Context) {
		// c.Param("filepath") 形如 "/assets/index-xxx.js" 或 "/users"。
		reqPath := strings.TrimPrefix(c.Param("filepath"), "/")
		if reqPath == "" {
			reqPath = "index.html"
		}
		// 存在则直出静态资源；否则回退 index.html（SPA 路由）。
		if _, err := fs.Stat(sub, reqPath); err != nil {
			c.Request.URL.Path = "/"
			serveIndex(c, sub)
			return
		}
		c.Request.URL.Path = "/" + reqPath
		fileServer.ServeHTTP(c.Writer, c.Request)
	}

	s.engine.GET("/console", func(c *gin.Context) { serveIndex(c, sub) })
	s.engine.GET("/console/*filepath", handler)
}

// serveIndex 直出 index.html（SPA 入口）。
func serveIndex(c *gin.Context, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		c.String(http.StatusNotFound, "控制台未构建：请先在 web/ 执行 npm run build")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}
