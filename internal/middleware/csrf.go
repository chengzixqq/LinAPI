package middleware

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// CSRFHeaderName 是前端回传 CSRF token 的请求头名。
const CSRFHeaderName = "X-CSRF-Token"

// CSRFCookieName 是下发给前端 JS 读取的 CSRF token Cookie 名（非 HttpOnly）。
const CSRFCookieName = "linapi_csrf"

// CSRFProtect 为 Cookie 鉴权的写操作提供 CSRF 防护（审查 AUD-P1-26），必须挂在
// SessionAuth 之后（依赖已注入的会话）。采用「会话绑定的双重提交」并叠加两道纵深防御：
//
//  1. 双重提交 token：写请求头 X-CSRF-Token 必须等于会话里存的 CSRFToken。攻击者从
//     跨站页面既读不到受害者的 csrf cookie，也无法设置自定义请求头，故无法伪造。
//  2. 强制 application/json：挡住攻击页用无需预检的 text/plain 简单表单构造合法 JSON body。
//  3. 精确同源 Origin：带 Origin 头时其 host 必须等于请求 Host，拦截同站异源攻击页
//     （SameSite=Strict 对同站不同子域仍会带 Cookie，故不能只靠 SameSite）。
//
// 只对有副作用的写方法（POST/PUT/PATCH/DELETE）校验；安全方法（GET/HEAD/OPTIONS）放行。
// 未注入会话（漏挂 SessionAuth）时一律拒绝——fail-closed。
func CSRFProtect() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isSafeMethod(c.Request.Method) {
			c.Next()
			return
		}

		// fail-closed：写操作必须有会话，且会话须带非空 CSRFToken。
		s, ok := SessionFrom(c)
		if !ok || s.CSRFToken == "" {
			abortError(c, http.StatusForbidden, "permission_error", "CSRF 校验失败")
			return
		}

		// 1. 强制 JSON Content-Type（只取媒体类型，忽略 charset 等参数）。
		if !isJSONContentType(c.GetHeader("Content-Type")) {
			abortError(c, http.StatusForbidden, "permission_error", "CSRF 校验失败：需 application/json")
			return
		}

		// 2. 有 Origin 头时必须精确同源。
		if origin := c.GetHeader("Origin"); origin != "" {
			if !sameOrigin(origin, c.Request.Host) {
				abortError(c, http.StatusForbidden, "permission_error", "CSRF 校验失败：跨源请求")
				return
			}
		}

		// 3. 双重提交 token 校验（常量时间比较，避免时序侧信道）。
		got := c.GetHeader(CSRFHeaderName)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.CSRFToken)) != 1 {
			abortError(c, http.StatusForbidden, "permission_error", "CSRF 校验失败：token 无效")
			return
		}

		c.Next()
	}
}

// isSafeMethod 判断是否为无副作用的安全方法（不需要 CSRF 校验）。
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// isJSONContentType 判断 Content-Type 的媒体类型是否为 application/json。
func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	// 去掉参数部分（如 "application/json; charset=utf-8"）。
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "application/json")
}

// sameOrigin 判断 Origin 头的 host 是否与请求 Host 精确相同。
// 只比较 host:port，不比较 scheme——请求侧无法可靠得知 Origin 的 scheme 是否匹配部署，
// 但 host 精确相等已足以拦截跨站/跨子域攻击页。
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == host
}
