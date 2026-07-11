package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"linapi/internal/account"
	"linapi/internal/admin"
	"linapi/internal/config"
	"linapi/internal/middleware"
	"linapi/internal/session"
	"linapi/internal/store"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// newAuthTestEngine 构建挂了 /auth 的 gin 引擎，返回引擎与底层依赖。
func newAuthTestEngine(t *testing.T) (*gin.Engine, account.AccountStore, *session.Manager) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sess := session.NewManager(rdb)

	accStore := account.NewMemoryStore(store.NewMemoryStore(nil))
	h := newAuthHandlers(accStore, accStore, sess, false, nil, nil)

	// 会话代次校验（审查 AUD-P1-17）：logout/me 走带代次的鉴权，账户禁用/改密后旧会话立即失效。
	verChecker := middleware.SessionVersionCheckerFunc(func(ctx context.Context, id int64) (int, error) {
		acc, err := accStore.GetByID(ctx, id)
		if err != nil {
			return 0, err
		}
		return acc.SessionVersion, nil
	})
	sessAuth := middleware.SessionAuthWithVersion(sess, verChecker)

	e := gin.New()
	g := e.Group("/auth")
	g.POST("/register", h.register)
	g.POST("/login", h.login)
	g.POST("/logout", sessAuth, h.logout)
	g.GET("/me", sessAuth, h.me)
	// 公开只读端点：登录页据此决定是否显示注册入口（匿名可达，无需鉴权）。
	g.GET("/registration-status", h.registrationStatus)
	return e, accStore, sess
}

func TestRegisterDisabledByDefault(t *testing.T) {
	e, _, _ := newAuthTestEngine(t)
	body, _ := json.Marshal(gin.H{"username": "alice", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("默认注册关闭应 403, 得到 %d", w.Code)
	}
}

// TestRegistrationStatusReflectsSetting 验证公开的 GET /auth/registration-status：
// 匿名可达（无需会话），如实反映 registration_enabled 开关，供登录页决定是否显示注册入口。
// 默认关闭返回 false；打开开关后返回 true。
func TestRegistrationStatusReflectsSetting(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)

	// 默认：注册关闭 → registration_enabled=false。
	req := httptest.NewRequest(http.MethodGet, "/auth/registration-status", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("registration-status 应 200, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	var got struct {
		RegistrationEnabled bool `json:"registration_enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应失败: %v; body=%s", err, w.Body.String())
	}
	if got.RegistrationEnabled {
		t.Fatalf("默认注册关闭时应返回 false, 得到 true")
	}

	// 打开注册开关后 → registration_enabled=true。
	_ = accStore.(*account.MemoryStore).Put(context.Background(), account.Settings{RegistrationEnabled: true})
	req = httptest.NewRequest(http.MethodGet, "/auth/registration-status", nil)
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("registration-status 应 200, 得到 %d", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.RegistrationEnabled {
		t.Fatalf("注册开启后应返回 true, 得到 false")
	}
}

func TestRegisterWhenEnabledThenLogin(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	// 打开注册开关。
	_ = accStore.(*account.MemoryStore).Put(context.Background(), account.Settings{RegistrationEnabled: true})

	body, _ := json.Marshal(gin.H{"username": "alice", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("开启后注册应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}

	// 登录应下发 Cookie。
	req = httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d", w.Code)
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("登录应下发会话 Cookie")
	}
}

func TestRegisterDoesNotRevealExistingUsername(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	_ = accStore.(*account.MemoryStore).Put(context.Background(), account.Settings{RegistrationEnabled: true})
	body, _ := json.Marshal(gin.H{"username": "same", "password": "password123"})

	responses := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/register", bytesReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("第 %d 次注册 status=%d body=%s", i+1, w.Code, w.Body.String())
		}
		responses = append(responses, w.Body.String())
	}
	if responses[0] != responses[1] {
		t.Fatalf("成功与用户名冲突响应必须一致: %q != %q", responses[0], responses[1])
	}
}

// TestRegisterGrantsNoBalance 验证自助注册恒不发放额度（审查 AUD-P0-07）：
// 即便系统设置里被注入正的 new_user_initial_balance（模拟脏配置 / 旧数据 /
// 绕过 putSettings 校验的直接 DB 写入），注册出来的账户余额也必须为 0——
// 否则任何人都能开一个账号克隆一笔免费额度，注册开关一开即被薅穿。
func TestRegisterGrantsNoBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sess := session.NewManager(rdb)

	base := store.NewMemoryStore(nil)
	accStore := account.NewMemoryStore(base)
	h := newAuthHandlers(accStore, accStore, sess, false, nil, nil)

	// 直接注入正初始额度，绕过 putSettings 校验，坐实“决定性修复”而非仅靠设置层拦截。
	_ = accStore.Put(context.Background(), account.Settings{
		RegistrationEnabled: true, NewUserInitialBalance: 1000,
	})

	e := gin.New()
	g := e.Group("/auth")
	g.POST("/register", h.register)

	body, _ := json.Marshal(gin.H{"username": "alice", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("开启注册后应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}

	// external_id = username；自助注册余额必须为 0。
	bal, err := base.Balance(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if bal != 0 {
		t.Fatalf("自助注册不得发放额度，余额应为 0，得到 %d", bal)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "bob", PasswordHash: hash, Role: account.RoleAdmin,
	})

	body, _ := json.Marshal(gin.H{"username": "bob", "password": "wrongpass"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401, 得到 %d", w.Code)
	}
}

func TestLoginIdentifierBudgetRunsBeforeAccountLookupAndBcrypt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	accStore := account.NewMemoryStore(store.NewMemoryStore(nil))
	h := newAuthHandlers(
		accStore, accStore, session.NewManager(rdb), false, nil,
		middleware.NewIdentifierRateLimiter(rdb, "test-credential", 1),
	)
	e := gin.New()
	e.POST("/auth/login", h.login)

	request := func(username string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(gin.H{"username": username, "password": "wrongpass"})
		req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		return w
	}
	if got := request("missing-user").Code; got != http.StatusUnauthorized {
		t.Fatalf("首次错误登录应维持统一 401，得到 %d", got)
	}
	if w := request(" MISSING-USER "); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("同一归一化登录名超配额应在查账户/bcrypt 前 429，status=%d retry=%q", w.Code, w.Header().Get("Retry-After"))
	}
	if got := request("other-user").Code; got != http.StatusUnauthorized {
		t.Fatalf("不同登录名不应共享预算，得到 %d", got)
	}
}

func TestLoginCookieAttributes(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "dave", PasswordHash: hash, Role: account.RoleAdmin,
	})

	body, _ := json.Marshal(gin.H{"username": "dave", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d", w.Code)
	}

	var sessionCookie *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == middleware.CookieName {
			sessionCookie = ck
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("未找到会话 Cookie")
	}
	if !sessionCookie.HttpOnly {
		t.Fatal("会话 Cookie 应为 HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("会话 Cookie SameSite 应为 Strict, 得到 %v", sessionCookie.SameSite)
	}
}

func TestLoginDisabledAccount(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	acc, err := accStore.CreateUserAccount(context.Background(), "eve", hash, 0)
	if err != nil {
		t.Fatalf("建账户失败: %v", err)
	}
	if _, err := accStore.SetEnabled(context.Background(), acc.ID, false); err != nil {
		t.Fatalf("禁用账户失败: %v", err)
	}

	body, _ := json.Marshal(gin.H{"username": "eve", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("禁用账户登录应 401, 得到 %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("用户名或密码错误")) {
		t.Fatalf("禁用账户的错误消息应与密码错误一致, 得到 body=%s", w.Body.String())
	}
}

func TestLogoutClearsSession(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "frank", PasswordHash: hash, Role: account.RoleAdmin,
	})

	login, _ := json.Marshal(gin.H{"username": "frank", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d", w.Code)
	}
	cookies := w.Result().Cookies()

	req = httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登出应 200, 得到 %d; body=%s", w.Code, w.Body.String())
	}

	// 同一个 Cookie 再访问 /auth/me，会话应已被删除。
	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("登出后 /auth/me 应 401, 得到 %d", w.Code)
	}
}

// TestLogoutFailsWhenSessionDeleteFails 验证会话删除失败时，logout 不得宣称登出成功、
// 不得清除 Cookie——否则用户以为已登出、本地 Cookie 消失，但被盗 token 在 Redis 恢复后
// 仍最长有效 7 天（审查 AUD-P1-29）。
func TestLogoutFailsWhenSessionDeleteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sess := session.NewManager(rdb)
	accStore := account.NewMemoryStore(store.NewMemoryStore(nil))
	h := newAuthHandlers(accStore, accStore, sess, false, nil, nil)

	// 先建一个真实会话拿到 token。
	token, err := sess.Create(context.Background(), session.SessionData{
		AccountID: 1, Username: "frank", Role: "admin",
	}, session.DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}

	// 只挂 logout（绕过 SessionAuth，直接验证 handler 在 Delete 失败下的行为）。
	e := gin.New()
	e.POST("/auth/logout", h.logout)

	// 关闭 Redis，使会话 Delete 必然失败。
	mr.Close()

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: middleware.CookieName, Value: token})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	// 删除失败不得返回 200（假登出）。
	if w.Code == http.StatusOK {
		t.Fatalf("会话删除失败时不应返回 200（假登出）, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	// 不得清除会话 Cookie（清了会让客户端误以为已登出，而服务端 token 仍有效）。
	for _, ck := range w.Result().Cookies() {
		if ck.Name == middleware.CookieName && ck.MaxAge < 0 {
			t.Fatal("删除失败时不应清除会话 Cookie（否则客户端误判已安全登出）")
		}
	}
}

// TestDisableAccountRevokesExistingSession 验证账户被禁用后，其登录时建立的旧会话
// 立即失效（审查 AUD-P1-17）：禁用使账户 session_version 递增，鉴权比对旧会话快照
// 不一致即 401。杜绝被禁用户/被盗 token 的旧 Cookie 继续可用。
func TestDisableAccountRevokesExistingSession(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	acc, err := accStore.CreateUserAccount(context.Background(), "grace", hash, 0)
	if err != nil {
		t.Fatalf("建账户失败: %v", err)
	}

	// 登录拿会话 Cookie。
	login, _ := json.Marshal(gin.H{"username": "grace", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()

	// 会话此刻有效：/auth/me 应 200。
	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("禁用前 /auth/me 应 200, 得到 %d", w.Code)
	}

	// 禁用账户 -> session_version 递增。
	if _, err := accStore.SetEnabled(context.Background(), acc.ID, false); err != nil {
		t.Fatalf("禁用账户失败: %v", err)
	}

	// 同一旧 Cookie 再访问：代次已变，旧会话应作废 -> 401。
	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("禁用账户后旧会话应失效 401, 得到 %d", w.Code)
	}
}

// TestPasswordResetRevokesExistingSession 验证改密后旧会话立即失效（审查 AUD-P1-17）：
// 改密使 session_version 递增，改密前建立的会话（含密码泄露期间被盗的）一律作废。
func TestPasswordResetRevokesExistingSession(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	acc, err := accStore.CreateUserAccount(context.Background(), "heidi", hash, 0)
	if err != nil {
		t.Fatalf("建账户失败: %v", err)
	}

	login, _ := json.Marshal(gin.H{"username": "heidi", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()

	// 改密 -> session_version 递增。
	newHash, _ := account.HashPassword("newpassword456")
	if err := accStore.UpdatePassword(context.Background(), acc.ID, newHash); err != nil {
		t.Fatalf("改密失败: %v", err)
	}

	// 改密前的旧会话应作废 -> 401。
	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧会话应失效 401, 得到 %d", w.Code)
	}
}

func TestMeReturnsIdentity(t *testing.T) {
	e, accStore, _ := newAuthTestEngine(t)
	hash, _ := account.HashPassword("password123")
	_, _ = accStore.CreateAccount(context.Background(), account.CreateAccountInput{
		Username: "carol", PasswordHash: hash, Role: account.RoleAdmin,
	})
	login, _ := json.Marshal(gin.H{"username": "carol", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(login))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	cookies := w.Result().Cookies()

	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/auth/me 应 200, 得到 %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["username"] != "carol" || got["role"] != "admin" {
		t.Fatalf("me 身份不符: %+v", got)
	}
}

// TestLoginRateLimitedByIP 是 P1-27 的接线断言：走真实 server.New 装配后，
// /auth/login 必须挂上按来源 IP 的限流中间件——同一 IP 超过 auth_rate_limit_per_min
// 后返回 429，把匿名撞库/CPU 耗尽挡在 bcrypt 之前。校验的是"限流真的接到了端点上"，
// 而非中间件本身（后者由 middleware 包单测覆盖）。
func TestLoginRateLimitedByIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	base := store.NewMemoryStore(nil)
	accStore := account.NewMemoryStore(base)
	sess := session.NewManager(rdb)

	const perMin = 3
	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	cfg.Admin.Enabled = true
	cfg.Admin.AuthRateLimitPerMin = perMin

	s := New(cfg, Deps{
		Store:    base,
		Redis:    rdb,
		Admin:    admin.NewService(admin.NewMemoryStore(base, nil), nil, nil),
		Account:  accStore,
		Settings: accStore,
		Session:  sess,
	})

	// 前 perMin 次(用户名/密码错误无所谓，限流在鉴权前生效)应放行进入 handler，
	// 返回 401（凭证错误）；第 perMin+1 次应被 IP 限流拦为 429。
	send := func() int {
		body, _ := json.Marshal(gin.H{"username": "nobody", "password": "whatever12"})
		req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.50:5555"
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < perMin; i++ {
		if code := send(); code == http.StatusTooManyRequests {
			t.Fatalf("第 %d 次(配额内)不应被限流, 得到 429", i+1)
		}
	}
	if code := send(); code != http.StatusTooManyRequests {
		t.Fatalf("超过 IP 配额应 429, 得到 %d", code)
	}
}

// TestCSRFProtectsMeWrites 是 P1-26 的端到端接线断言：走真实 server.New 装配后，
// 登录返回会话 Cookie + csrf_token；随后对 /me 的写操作（POST /me/keys）——
//   - 带正确 X-CSRF-Token → 放行（201）；
//   - 不带 token → 被 CSRFProtect 拦为 403。
//
// 校验的是"CSRF 中间件真的接到了 /me 写端点上、且 token 与登录会话绑定"。
func TestCSRFProtectsMeWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	base := store.NewMemoryStore(nil)
	accStore := account.NewMemoryStore(base)
	sess := session.NewManager(rdb)

	// 预置一个可登录账户（CreateUserAccount 原子连带建计费实体，供建 key 归属）。
	hash, _ := account.HashPassword("password123")
	if _, err := accStore.CreateUserAccount(context.Background(), "csrfuser", hash, 0); err != nil {
		t.Fatalf("预置账户失败: %v", err)
	}

	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	cfg.Admin.Enabled = true

	s := New(cfg, Deps{
		Store:    base,
		Redis:    rdb,
		Admin:    admin.NewService(admin.NewMemoryStore(base, nil), nil, nil),
		Account:  accStore,
		Settings: accStore,
		Session:  sess,
	})

	// 登录，拿会话 Cookie 与 csrf_token。
	loginBody, _ := json.Marshal(gin.H{"username": "csrfuser", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytesReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, 得到 %d; body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var loginResp struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &loginResp)
	if loginResp.CSRFToken == "" {
		t.Fatal("登录响应应含 csrf_token")
	}

	// 带正确 X-CSRF-Token 的建 key 写请求 → 放行（201）。
	keyBody, _ := json.Marshal(gin.H{"rate_limit_per_min": 60})
	req = httptest.NewRequest(http.MethodPost, "/me/keys", bytesReader(keyBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(middleware.CSRFHeaderName, loginResp.CSRFToken)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("带正确 CSRF token 的写请求应 201, 得到 %d; body=%s", w.Code, w.Body.String())
	}

	// 不带 X-CSRF-Token 的同样写请求 → 403（即便会话 Cookie 有效）。
	req = httptest.NewRequest(http.MethodPost, "/me/keys", bytesReader(keyBody))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w = httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("缺 CSRF token 的写请求应被拦为 403, 得到 %d; body=%s", w.Code, w.Body.String())
	}
}
