// 与后端响应结构对齐的 TS 类型。金额为最小计费单位（整数）。

export interface MeInfo {
  username: string
  role: 'admin' | 'user'
  external_id: string
}

// 注意：凡是要喂给 DataTable / Semi Table 的行模型，都用 type 别名而非 interface。
// interface 不带隐式索引签名（因可被声明合并扩展），无法满足 Table 的
// `RecordType extends Record<string, any>` 约束；type 别名可以。改回 interface 会破坏构建。
export type Account = {
  id: number
  username: string
  role: 'admin' | 'user'
  external_id?: string
  group_name: string
  enabled: boolean
  created_at: string
}

export interface Settings {
  registration_enabled: boolean
  /** @deprecated AUD-P0-07：注册不发额度，后端恒 0 且拒绝非 0 写入。前端不再提供输入框。 */
  new_user_initial_balance?: number
}

/** 登录/注册响应。csrf_token 冗余回显（读 cookie 路线下前端不依赖，保留以备用）。 */
export interface AuthResp {
  username: string
  role: 'admin' | 'user'
  csrf_token?: string
}

export type User = {
  external_id: string
  balance: number
  enabled: boolean
  created_at: string
  updated_at: string
}

export type APIKey = {
  key_id: string
  user_id: string
  rate_limit_per_min: number
  allowed_models: string[]
  enabled: boolean
  created_at: string
}

export type Channel = {
  channel_id: string
  name: string
  format: 'openai' | 'anthropic'
  base_url: string
  models: Record<string, string>
  priority: number
  weight: number
  enabled: boolean
  created_at: string
  updated_at: string
}

// 创建密钥的响应：唯一一次带明文 api_key。
export type CreatedKey = APIKey & {
  api_key: string
}

// 列表端点统一包在 { data: [...] } 里。
export interface ListResp<T> {
  data: T[]
}
