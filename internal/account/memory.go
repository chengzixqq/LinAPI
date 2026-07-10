package account

import (
	"context"
	"sort"
	"sync"
	"time"

	"linapi/internal/store"
)

// MemoryStore 是 AccountStore + SettingsStore 的内存实现（database.enabled=false 时用）。
//
// 计费实体复用注入的 store.MemoryStore（建 user 账户时在其上建计费用户），
// 使账户体系与热路径共享同一份用户/余额数据。账户与设置由本类型自持。
type MemoryStore struct {
	base *store.MemoryStore

	mu       sync.RWMutex
	byID     map[int64]*memAccount
	byName   map[string]*memAccount
	nextID   int64
	settings Settings
}

type memAccount struct {
	acc  Account
	hash string
}

// NewMemoryStore 包装一个 store.MemoryStore。
func NewMemoryStore(base *store.MemoryStore) *MemoryStore {
	return &MemoryStore{
		base:     base,
		byID:     make(map[int64]*memAccount),
		byName:   make(map[string]*memAccount),
		nextID:   1,
		settings: Settings{RegistrationEnabled: DefaultRegistrationEnabled, NewUserInitialBalance: DefaultNewUserInitialBalance},
	}
}

// CreateUserAccount 建 user 账户 + 计费实体（external_id 用 username）。
func (m *MemoryStore) CreateUserAccount(_ context.Context, username, passwordHash string, initialBalance int64) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byName[username]; ok {
		return Account{}, ErrConflict
	}
	// 先建计费实体（external_id = username）；失败则不建账户（不留孤儿）。
	if _, err := m.base.AdminCreateUser(username, initialBalance, true); err != nil {
		// 计费实体已存在等同用户名冲突。
		return Account{}, ErrConflict
	}
	return m.insertLocked(username, passwordHash, RoleUser, username), nil
}

// CreateAccount 直接建账户（bootstrap 建 admin，不连带计费实体）。
func (m *MemoryStore) CreateAccount(_ context.Context, in CreateAccountInput) (Account, error) {
	if !ValidRole(in.Role) {
		return Account{}, ErrInvalidRole
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byName[in.Username]; ok {
		return Account{}, ErrConflict
	}
	return m.insertLocked(in.Username, in.PasswordHash, in.Role, in.ExternalID), nil
}

// insertLocked 在持锁下插入账户，返回视图。调用方须已校验用户名唯一。
func (m *MemoryStore) insertLocked(username, hash, role, externalID string) Account {
	acc := Account{
		ID:         m.nextID,
		Username:   username,
		Role:       role,
		ExternalID: externalID,
		GroupName:  "default",
		Enabled:    true,
		CreatedAt:  time.Now(),
	}
	rec := &memAccount{acc: acc, hash: hash}
	m.byID[acc.ID] = rec
	m.byName[username] = rec
	m.nextID++
	return acc
}

func (m *MemoryStore) GetCredentials(_ context.Context, username string) (Credentials, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byName[username]
	if !ok {
		return Credentials{}, ErrNotFound
	}
	return Credentials{Account: rec.acc, PasswordHash: rec.hash}, nil
}

func (m *MemoryStore) GetByID(_ context.Context, id int64) (Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byID[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return rec.acc, nil
}

func (m *MemoryStore) GetByUsername(_ context.Context, username string) (Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byName[username]
	if !ok {
		return Account{}, ErrNotFound
	}
	return rec.acc, nil
}

func (m *MemoryStore) ListAccounts(_ context.Context, limit, offset int) ([]Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := make([]Account, 0, len(m.byID))
	for _, rec := range m.byID {
		all = append(all, rec.acc)
	}
	// 按 ID 倒序（近似创建时间倒序）。
	sortAccountsDesc(all)
	return pageAccounts(all, limit, offset), nil
}

func (m *MemoryStore) CountAccounts(_ context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int64(len(m.byID)), nil
}

func (m *MemoryStore) SetEnabled(_ context.Context, id int64, enabled bool) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.byID[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	rec.acc.Enabled = enabled
	// 禁用递增会话代次，使旧会话立即失效（审查 AUD-P1-17）；重新启用无需踢已在线会话。
	if !enabled {
		rec.acc.SessionVersion++
	}
	return rec.acc, nil
}

func (m *MemoryStore) UpdatePassword(_ context.Context, id int64, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.byID[id]
	if !ok {
		return ErrNotFound
	}
	rec.hash = passwordHash
	// 改密递增会话代次，使旧会话（含密码泄露期间建立的）立即失效（审查 AUD-P1-17）。
	rec.acc.SessionVersion++
	return nil
}

func (m *MemoryStore) Get(_ context.Context) (Settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings, nil
}

func (m *MemoryStore) Put(_ context.Context, s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = s
	return nil
}

// sortAccountsDesc 按 ID 倒序（近似创建时间倒序）。
func sortAccountsDesc(a []Account) {
	sort.Slice(a, func(i, j int) bool { return a[i].ID > a[j].ID })
}

// pageAccounts 应用 limit/offset（limit<=0 不限制）。
func pageAccounts(a []Account, limit, offset int) []Account {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(a) {
		return []Account{}
	}
	a = a[offset:]
	if limit > 0 && limit < len(a) {
		a = a[:limit]
	}
	return a
}

// 编译期断言：MemoryStore 同时实现两个接口。
var (
	_ AccountStore  = (*MemoryStore)(nil)
	_ SettingsStore = (*MemoryStore)(nil)
)
