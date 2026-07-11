import { apiFetch } from './client'
import type {
  Account, APIKey, AuthResp, Channel, CreatedKey, ListResp, MeInfo, Settings, User,
} from './types'

// 按域聚合的端点封装。所有写操作走 JSON body。
export const api = {
  auth: {
    login: (username: string, password: string, remember: boolean) =>
      apiFetch<AuthResp>('/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password, remember }),
      }),
    register: (username: string, password: string) =>
      apiFetch<AuthResp>('/auth/register', {
        method: 'POST',
        body: JSON.stringify({ username, password }),
      }),
    logout: () => apiFetch<{ ok: boolean }>('/auth/logout', { method: 'POST' }),
    me: () => apiFetch<MeInfo>('/auth/me'),
  },
  admin: {
    listUsers: (limit = 100, offset = 0) =>
      apiFetch<ListResp<User>>(`/admin/users?limit=${limit}&offset=${offset}`),
    createUser: (external_id: string, balance: number) =>
      apiFetch<User>('/admin/users', {
        method: 'POST',
        body: JSON.stringify({ external_id, balance }),
      }),
    setUserEnabled: (id: string, enabled: boolean) =>
      apiFetch<User>(`/admin/users/${encodeURIComponent(id)}/enabled`, {
        method: 'PATCH',
        body: JSON.stringify({ enabled }),
      }),
    addBalance: (id: string, delta: number) =>
      apiFetch<{ external_id: string; balance: number }>(
        `/admin/users/${encodeURIComponent(id)}/balance`,
        { method: 'POST', body: JSON.stringify({ delta }) },
      ),
    listKeys: (userId: string) =>
      apiFetch<ListResp<APIKey>>(`/admin/users/${encodeURIComponent(userId)}/keys`),
    createKey: (userId: string, rate_limit_per_min: number, allowed_models: string[]) =>
      apiFetch<CreatedKey>(`/admin/users/${encodeURIComponent(userId)}/keys`, {
        method: 'POST',
        body: JSON.stringify({ rate_limit_per_min, allowed_models }),
      }),
    setKeyEnabled: (keyId: string, enabled: boolean) =>
      apiFetch<APIKey>(`/admin/keys/${encodeURIComponent(keyId)}/enabled`, {
        method: 'PATCH',
        body: JSON.stringify({ enabled }),
      }),
    listChannels: () => apiFetch<ListResp<Channel>>('/admin/channels'),
    createChannel: (body: Partial<Channel> & { api_key: string }) =>
      apiFetch<Channel>('/admin/channels', { method: 'POST', body: JSON.stringify(body) }),
    updateChannel: (id: string, body: Partial<Channel> & { api_key: string }) =>
      apiFetch<Channel>(`/admin/channels/${encodeURIComponent(id)}`, {
        method: 'PUT', body: JSON.stringify(body),
      }),
    setChannelEnabled: (id: string, enabled: boolean) =>
      apiFetch<Channel>(`/admin/channels/${encodeURIComponent(id)}/enabled`, {
        method: 'PATCH', body: JSON.stringify({ enabled }),
      }),
    deleteChannel: (id: string) =>
      apiFetch<void>(`/admin/channels/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    listAccounts: (limit = 100, offset = 0) =>
      apiFetch<ListResp<Account>>(`/admin/accounts?limit=${limit}&offset=${offset}`),
    createAccount: (body: { username: string; password: string; role: string; initial_balance?: number }) =>
      apiFetch<Account>('/admin/accounts', { method: 'POST', body: JSON.stringify(body) }),
    setAccountEnabled: (id: number, enabled: boolean) =>
      apiFetch<Account>(`/admin/accounts/${id}/enabled`, {
        method: 'PATCH', body: JSON.stringify({ enabled }),
      }),
    resetPassword: (id: number, password: string) =>
      apiFetch<{ ok: boolean }>(`/admin/accounts/${id}/password`, {
        method: 'POST', body: JSON.stringify({ password }),
      }),
    getSettings: () => apiFetch<Settings>('/admin/settings'),
    putSettings: (s: Settings) =>
      apiFetch<Settings>('/admin/settings', { method: 'PUT', body: JSON.stringify(s) }),
  },
  me: {
    profile: () => apiFetch<{ external_id: string; balance: number }>('/me/profile'),
    listKeys: () => apiFetch<ListResp<APIKey>>('/me/keys'),
    createKey: (rate_limit_per_min: number, allowed_models: string[]) =>
      apiFetch<CreatedKey>('/me/keys', {
        method: 'POST',
        body: JSON.stringify({ rate_limit_per_min, allowed_models }),
      }),
    setKeyEnabled: (keyId: string, enabled: boolean) =>
      apiFetch<APIKey>(`/me/keys/${encodeURIComponent(keyId)}/enabled`, {
        method: 'PATCH', body: JSON.stringify({ enabled }),
      }),
    deleteKey: (keyId: string) =>
      apiFetch<void>(`/me/keys/${encodeURIComponent(keyId)}`, { method: 'DELETE' }),
  },
}
