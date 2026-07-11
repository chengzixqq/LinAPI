// 统一 fetch 封装：始终带同源 Cookie（会话），把非 2xx 归一为 ApiError，
// 401 特化为 UnauthorizedError 供全局拦截（清登录态、踢回登录页）。

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
    this.name = 'ApiError'
  }
}

export class UnauthorizedError extends ApiError {
  constructor(message: string) {
    super(401, message)
    this.name = 'UnauthorizedError'
  }
}

// 401 全局订阅：auth store 注册回调，收到即清态跳登录。
type UnauthorizedHandler = () => void
let onUnauthorized: UnauthorizedHandler | null = null
export function setUnauthorizedHandler(fn: UnauthorizedHandler) {
  onUnauthorized = fn
}

// 从 document.cookie 读非 HttpOnly 的 CSRF token（登录后由后端下发 linapi_csrf）。
// 读 cookie 而非登录响应体：刷新页面 cookie 仍在，无需前端内存态；登出清 cookie 后自然失效。
function readCsrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)linapi_csrf=([^;]*)/)
  return m ? decodeURIComponent(m[1]) : ''
}

// 非安全方法需带 CSRF token（AUD-P1-26 双重提交）；GET/HEAD/OPTIONS 后端自动放行。
const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const method = (init?.method ?? 'GET').toUpperCase()
  const csrfHeader: Record<string, string> =
    SAFE_METHODS.has(method) ? {} : { 'X-CSRF-Token': readCsrfToken() }
  const resp = await fetch(path, {
    ...init,
    credentials: 'include', // 带 HttpOnly 会话 Cookie。
    headers: {
      'Content-Type': 'application/json', // 后端 CSRF 强制 application/json。
      ...csrfHeader,
      ...(init?.headers ?? {}),
    },
  })

  if (resp.status === 401) {
    if (onUnauthorized) onUnauthorized()
    throw new UnauthorizedError('会话已失效，请重新登录')
  }

  if (!resp.ok) {
    // 后端错误结构：{ error: { message, type } }。
    let message = `请求失败（${resp.status}）`
    try {
      const body = await resp.json()
      if (body?.error?.message) message = body.error.message
    } catch {
      /* 忽略解析失败，用兜底文案 */
    }
    throw new ApiError(resp.status, message)
  }

  if (resp.status === 204) return undefined as T
  return (await resp.json()) as T
}
