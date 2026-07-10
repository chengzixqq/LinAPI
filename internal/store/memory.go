package store

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

// MemoryStore 是 Store 的内存实现，数据来自配置或测试注入。
// 用于开发期与单元测试；生产环境将由 sqlc/PostgreSQL 实现替换（第 7 步）。
//
// 并发安全：所有读通过 RWMutex 保护。余额支持原子增减，供计费模块过渡期使用。
//
// 除 Store 接口外，MemoryStore 还提供一组管理操作（Admin* 方法），
// 供 internal/admin 的内存实现复用，使内存模式下「管理面创建的用户/密钥」
// 能被热路径鉴权/额度中间件即时读到（共享同一份数据）。
type MemoryStore struct {
	mu sync.RWMutex

	// keys 以明文 API Key 为索引存储身份。
	keys map[string]*Identity

	// keyByID 以 KeyID 为索引指向同一 Identity，供管理面按 KeyID 启停。
	keyByID map[string]*Identity

	// balances 以 userID 为索引存储额度余额。
	balances map[string]int64

	// users 记录用户的启停与创建时间（管理面用）。
	users map[string]*memUser
}

// memUser 是内存模式下的用户元数据。
type memUser struct {
	enabled   bool
	createdAt time.Time
	updatedAt time.Time
}

// KeySeed 描述一个预置密钥，用于从配置构建 MemoryStore。
type KeySeed struct {
	APIKey          string
	KeyID           string
	UserID          string
	RateLimitPerMin int
	AllowedModels   []string
	Enabled         bool
	// InitialBalance 是该密钥所属用户的初始余额（多个同 UserID 的种子取首次出现值）。
	InitialBalance int64
}

// NewMemoryStore 用一组种子密钥构建内存存储。
func NewMemoryStore(seeds []KeySeed) *MemoryStore {
	s := &MemoryStore{
		keys:     make(map[string]*Identity, len(seeds)),
		keyByID:  make(map[string]*Identity, len(seeds)),
		balances: make(map[string]int64),
		users:    make(map[string]*memUser),
	}
	for _, seed := range seeds {
		if _, exists := s.keys[seed.APIKey]; exists {
			panic("store: 配置中存在重复 API Key")
		}
		if seed.KeyID != "" {
			if _, exists := s.keyByID[seed.KeyID]; exists {
				panic("store: 配置中存在重复 key_id: " + seed.KeyID)
			}
		}
		now := time.Now()
		id := &Identity{
			KeyID:           seed.KeyID,
			UserID:          seed.UserID,
			RateLimitPerMin: seed.RateLimitPerMin,
			AllowedModels:   seed.AllowedModels,
			Enabled:         seed.Enabled,
			CreatedAt:       now,
		}
		s.keys[seed.APIKey] = id
		if seed.KeyID != "" {
			s.keyByID[seed.KeyID] = id
		}
		// 同一用户仅初始化一次余额与元数据。
		if _, ok := s.users[seed.UserID]; !ok {
			s.balances[seed.UserID] = seed.InitialBalance
			s.users[seed.UserID] = &memUser{enabled: true, createdAt: now, updatedAt: now}
		}
	}
	return s
}

// ResolveKey 实现 Store 接口。
func (s *MemoryStore) ResolveKey(_ context.Context, apiKey string) (*Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.keys[apiKey]
	if !ok || !id.Enabled {
		return nil, ErrKeyNotFound
	}
	// 用户被禁用时密钥同样不可用（对齐 PG 实现的联表过滤）。
	if u, ok := s.users[id.UserID]; ok && !u.enabled {
		return nil, ErrKeyNotFound
	}
	// 返回副本，避免调用方修改内部状态。
	cp := *id
	return &cp, nil
}

// Balance 实现 Store 接口。
func (s *MemoryStore) Balance(_ context.Context, userID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.balances[userID], nil
}

// AddBalance 原子增减某用户余额并拒绝 int64 回绕。delta 为负表示扣费。
func (s *MemoryStore) AddBalance(userID string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bal, err := checkedBalanceAdd(s.balances[userID], delta)
	if err != nil {
		return s.balances[userID], err
	}
	s.balances[userID] = bal
	if u := s.users[userID]; u != nil {
		u.updatedAt = time.Now()
	}
	return bal, nil
}

// BillingReserve 为内存账本原子冻结一笔余额。仅供 database.enabled=false 的
// 开发模式使用；生产资金路径由 PostgreSQL 事务账本实现。
func (s *MemoryStore) BillingReserve(userID string, amount int64) (bool, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if amount < 0 {
		return false, 0, ErrBalanceOverflow
	}
	u, ok := s.users[userID]
	if !ok || !u.enabled {
		return false, 0, ErrUserNotFound
	}
	bal := s.balances[userID]
	if bal < amount {
		return false, bal, nil
	}
	bal -= amount
	s.balances[userID] = bal
	return true, bal, nil
}

// BillingAdjust 为内存账本原子应用结算/退款差额并拒绝 int64 回绕。
func (s *MemoryStore) BillingAdjust(userID string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[userID]; !ok {
		return 0, ErrUserNotFound
	}
	bal := s.balances[userID]
	bal, err := checkedBalanceAdd(bal, delta)
	if err != nil {
		return 0, err
	}
	s.balances[userID] = bal
	return bal, nil
}

// ---- 管理操作 ----
//
// 以下方法供 internal/admin 的内存实现复用，使内存模式下管理面写入的
// 用户/密钥能被热路径鉴权/额度即时读到。用 sentinel error 表达冲突/不存在，
// 由 admin 层映射为其领域错误。

// ErrUserExists / ErrUserNotFound / ErrKeyExists 是管理操作的 sentinel。
var (
	ErrUserExists      = errUserExists
	ErrUserNotFound    = errUserNotFound
	ErrKeyExists       = errKeyExists
	ErrBalanceOverflow = errors.New("store: 余额算术溢出")
)

// MemUserView 是管理面读取用户的中性视图（不引入跨包类型依赖）。
type MemUserView struct {
	ExternalID string
	Balance    int64
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// MemKeyView 是管理面读取密钥的中性视图。
type MemKeyView struct {
	KeyID           string
	UserID          string
	RateLimitPerMin int
	AllowedModels   []string
	Enabled         bool
	CreatedAt       time.Time
}

// AdminCreateUser 新建用户；external_id 重复返回 ErrUserExists。
func (s *MemoryStore) AdminCreateUser(externalID string, balance int64, enabled bool) (MemUserView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[externalID]; ok {
		return MemUserView{}, errUserExists
	}
	now := time.Now()
	s.users[externalID] = &memUser{enabled: enabled, createdAt: now, updatedAt: now}
	s.balances[externalID] = balance
	return MemUserView{ExternalID: externalID, Balance: balance, Enabled: enabled, CreatedAt: now, UpdatedAt: now}, nil
}

// AdminListUsers 分页列出用户（按创建时间倒序）。
func (s *MemoryStore) AdminListUsers(limit, offset int) []MemUserView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := make([]MemUserView, 0, len(s.users))
	for id, u := range s.users {
		views = append(views, MemUserView{
			ExternalID: id, Balance: s.balances[id], Enabled: u.enabled,
			CreatedAt: u.createdAt, UpdatedAt: u.updatedAt,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].CreatedAt.Equal(views[j].CreatedAt) {
			return views[i].ExternalID < views[j].ExternalID
		}
		return views[i].CreatedAt.After(views[j].CreatedAt)
	})
	return paginate(views, limit, offset)
}

// AdminGetUser 取单个用户；不存在返回 ErrUserNotFound。
func (s *MemoryStore) AdminGetUser(externalID string) (MemUserView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[externalID]
	if !ok {
		return MemUserView{}, errUserNotFound
	}
	return MemUserView{ExternalID: externalID, Balance: s.balances[externalID], Enabled: u.enabled, CreatedAt: u.createdAt, UpdatedAt: u.updatedAt}, nil
}

// AdminSetUserEnabled 启停用户；不存在返回 ErrUserNotFound。
func (s *MemoryStore) AdminSetUserEnabled(externalID string, enabled bool) (MemUserView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[externalID]
	if !ok {
		return MemUserView{}, errUserNotFound
	}
	u.enabled = enabled
	u.updatedAt = time.Now()
	return MemUserView{ExternalID: externalID, Balance: s.balances[externalID], Enabled: u.enabled, CreatedAt: u.createdAt, UpdatedAt: u.updatedAt}, nil
}

// AdminAddBalance 充值/扣减用户余额并返回新值；用户不存在返回 ErrUserNotFound。
func (s *MemoryStore) AdminAddBalance(externalID string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[externalID]; !ok {
		return 0, errUserNotFound
	}
	bal, err := checkedBalanceAdd(s.balances[externalID], delta)
	if err != nil {
		return 0, err
	}
	s.balances[externalID] = bal
	s.users[externalID].updatedAt = time.Now()
	return bal, nil
}

func checkedBalanceAdd(balance, delta int64) (int64, error) {
	if (delta > 0 && balance > math.MaxInt64-delta) ||
		(delta < 0 && balance < math.MinInt64-delta) {
		return 0, ErrBalanceOverflow
	}
	return balance + delta, nil
}

// AdminCreateKey 新建密钥；key_id 重复返回 ErrKeyExists，用户不存在返回 ErrUserNotFound。
func (s *MemoryStore) AdminCreateKey(apiKey, keyID, userID string, rateLimit int, allowedModels []string, enabled bool) (MemKeyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adminCreateKeyLocked(apiKey, keyID, userID, rateLimit, allowedModels, enabled)
}

// AdminCreateKeyLimited 在同一把锁内完成计数与创建，避免并发请求同时越过上限。
func (s *MemoryStore) AdminCreateKeyLimited(apiKey, keyID, userID string, rateLimit int, allowedModels []string, enabled bool, maxKeys int) (MemKeyView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxKeys > 0 {
		count := 0
		for _, id := range s.keyByID {
			if id.UserID == userID {
				count++
			}
		}
		if count >= maxKeys {
			return MemKeyView{}, false, nil
		}
	}
	view, err := s.adminCreateKeyLocked(apiKey, keyID, userID, rateLimit, allowedModels, enabled)
	return view, err == nil, err
}

func (s *MemoryStore) adminCreateKeyLocked(apiKey, keyID, userID string, rateLimit int, allowedModels []string, enabled bool) (MemKeyView, error) {
	if _, ok := s.users[userID]; !ok {
		return MemKeyView{}, errUserNotFound
	}
	if _, ok := s.keyByID[keyID]; ok {
		return MemKeyView{}, errKeyExists
	}
	if _, ok := s.keys[apiKey]; ok {
		return MemKeyView{}, errKeyExists
	}
	now := time.Now()
	id := &Identity{
		KeyID:           keyID,
		UserID:          userID,
		RateLimitPerMin: rateLimit,
		AllowedModels:   allowedModels,
		Enabled:         enabled,
		CreatedAt:       now,
	}
	s.keys[apiKey] = id
	s.keyByID[keyID] = id
	return MemKeyView{
		KeyID: keyID, UserID: userID, RateLimitPerMin: rateLimit,
		AllowedModels: allowedModels, Enabled: enabled, CreatedAt: now,
	}, nil
}

// AdminListKeysByUser 列出某用户的全部密钥。
func (s *MemoryStore) AdminListKeysByUser(userID string) []MemKeyView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := []MemKeyView{}
	for _, id := range s.keyByID {
		if id.UserID != userID {
			continue
		}
		views = append(views, MemKeyView{
			KeyID: id.KeyID, UserID: id.UserID, RateLimitPerMin: id.RateLimitPerMin,
			AllowedModels: id.AllowedModels, Enabled: id.Enabled, CreatedAt: id.CreatedAt,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].KeyID < views[j].KeyID })
	return views
}

// AdminSetKeyEnabled 启停密钥；不存在返回 ErrKeyNotFound。
func (s *MemoryStore) AdminSetKeyEnabled(keyID string, enabled bool) (MemKeyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.keyByID[keyID]
	if !ok {
		return MemKeyView{}, ErrKeyNotFound
	}
	id.Enabled = enabled
	return MemKeyView{
		KeyID: id.KeyID, UserID: id.UserID, RateLimitPerMin: id.RateLimitPerMin,
		AllowedModels: id.AllowedModels, Enabled: id.Enabled, CreatedAt: id.CreatedAt,
	}, nil
}

// AdminDeleteKey 物理删除密钥（按 keyID）；不存在返回 ErrKeyNotFound。
func (s *MemoryStore) AdminDeleteKey(keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.keyByID[keyID]
	if !ok {
		return ErrKeyNotFound
	}
	delete(s.keyByID, keyID)
	// 同步删除明文 key 索引（找到指向同一 Identity 的条目）。
	for k, v := range s.keys {
		if v == id {
			delete(s.keys, k)
			break
		}
	}
	return nil
}

// paginate 对切片应用 limit/offset（limit<=0 表示不限制）。
func paginate(views []MemUserView, limit, offset int) []MemUserView {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(views) {
		return []MemUserView{}
	}
	views = views[offset:]
	if limit > 0 && limit < len(views) {
		views = views[:limit]
	}
	return views
}
