import { createContext, useContext, useEffect, useState, useCallback, type ReactNode } from 'react'
import { api } from '../api/endpoints'
import { setUnauthorizedHandler, UnauthorizedError } from '../api/client'
import type { MeInfo } from '../api/types'

interface AuthState {
  me: MeInfo | null
  loading: boolean          // 初次 /auth/me 探活未回时为 true。
  refresh: () => Promise<void>
  logout: () => Promise<void>
  setMe: (m: MeInfo) => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<MeInfo | null>(null)
  const [loading, setLoading] = useState(true)

  // 探活：HttpOnly Cookie 前端读不到，登录态只能问后端。
  const refresh = useCallback(async () => {
    try {
      const info = await api.auth.me()
      setMe(info)
    } catch (e) {
      if (e instanceof UnauthorizedError) setMe(null)
      // 其它错误（网络/500）保持当前态，交由页面错误态处理。
    } finally {
      setLoading(false)
    }
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.auth.logout()
    } finally {
      setMe(null)
    }
  }, [])

  // 全局 401 拦截：任何请求 401 → 清态（ProtectedRoute 随即跳登录）。
  useEffect(() => {
    setUnauthorizedHandler(() => setMe(null))
  }, [])

  // 首次挂载探活。
  useEffect(() => {
    void refresh()
  }, [refresh])

  return (
    <AuthContext.Provider value={{ me, loading, refresh, logout, setMe }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth 必须在 AuthProvider 内使用')
  return ctx
}
