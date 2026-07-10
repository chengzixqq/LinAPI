package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"linapi/internal/billing"
	"linapi/internal/store"
)

// ctxKeyReservation 是预扣费句柄注入 gin.Context 的键。
const ctxKeyReservation = "linapi.reservation"

// Quota 构建额度中间件：请求进入业务处理前，用 Redis 原子预扣一笔押金（预授权）。
//
// 流程：
//  1. 读冷源（store）余额作为 seed；
//  2. billing.Reserve 原子预扣 default_reserve（余额不足则 402 拦截）；
//  3. 预扣句柄注入 context，转发层完成后取出并 Settle（按真实用量退差）。
//
// 预扣时尚未解析请求体，故 model 留空；转发 handler 解析出真实模型后，
// 用 SetReservationModel 补上，供 Settle 计价。
func Quota(s store.Store, b *billing.Billing) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := IdentityFrom(c)
		if !ok {
			abortError(c, http.StatusUnauthorized, "authentication_error", "未鉴权")
			return
		}

		// 冷源余额作为 Redis 惰性初始化的 seed。
		seed, err := s.Balance(c.Request.Context(), id.UserID)
		if err != nil {
			abortError(c, http.StatusInternalServerError, "internal_error",
				"额度服务暂时不可用")
			return
		}

		// 原子预扣押金。model 此刻未知，Settle 前由转发层补上。
		res, ok, err := b.Reserve(c.Request.Context(), id.UserID, id.KeyID, "", seed)
		if err != nil {
			abortError(c, http.StatusInternalServerError, "internal_error",
				"计费服务暂时不可用")
			return
		}
		if !ok {
			abortError(c, http.StatusPaymentRequired, "insufficient_quota",
				"额度不足，请充值后重试")
			return
		}

		c.Set(ctxKeyReservation, res)
		c.Next()
	}
}

// ReservationFrom 从 gin.Context 取出预扣费句柄（供转发层结算）。
func ReservationFrom(c *gin.Context) (billing.Reservation, bool) {
	v, ok := c.Get(ctxKeyReservation)
	if !ok {
		return billing.Reservation{}, false
	}
	r, ok := v.(billing.Reservation)
	return r, ok
}

// SetReservationModel 在转发层解析出真实模型名后回填到预扣句柄，供 Settle 计价。
func SetReservationModel(c *gin.Context, model string) {
	r, ok := ReservationFrom(c)
	if !ok {
		return
	}
	r.Model = model
	c.Set(ctxKeyReservation, r)
}
