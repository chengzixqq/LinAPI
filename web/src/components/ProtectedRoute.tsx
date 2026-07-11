import { Navigate, useLocation } from 'react-router-dom'
import { Center, Loader } from '@mantine/core'
import type { ReactNode } from 'react'
import { useAuth } from '../stores/auth'

// ProtectedRoute 是登录/角色守护。仅 UX 层——真正授权在后端。
// loading 时显示全屏 loader（避免闪回登录页）；未登录跳 /login；
// 角色不符跳各自首页（admin→/overview，user→/portal）。
export function ProtectedRoute({ role, children }: { role?: 'admin' | 'user'; children: ReactNode }) {
  const { me, loading } = useAuth()
  const location = useLocation()

  if (loading) {
    return (
      <Center h="100vh">
        <Loader size="lg" />
      </Center>
    )
  }
  if (!me) {
    return <Navigate to="/login" replace state={{ from: location }} />
  }
  if (role && me.role !== role) {
    return <Navigate to={me.role === 'admin' ? '/overview' : '/portal'} replace />
  }
  return <>{children}</>
}
