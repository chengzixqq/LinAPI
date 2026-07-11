import { Routes, Route, Navigate } from 'react-router-dom'
import { ProtectedRoute } from './components/ProtectedRoute'
import { AppLayout } from './components/Layout'
import Login from './pages/Login'
import Register from './pages/Register'
import Overview from './pages/admin/Overview'
import Users from './pages/admin/Users'
import Channels from './pages/admin/Channels'
import Accounts from './pages/admin/Accounts'
import Settings from './pages/admin/Settings'
import PortalHome from './pages/portal/PortalHome'
import PortalKeys from './pages/portal/PortalKeys'

export default function App() {
  return (
    <Routes>
      {/* 公共 */}
      <Route path="/login" element={<Login />} />
      <Route path="/register" element={<Register />} />

      {/* admin 区 */}
      <Route element={<ProtectedRoute role="admin"><AppLayout /></ProtectedRoute>}>
        <Route path="/overview" element={<Overview />} />
        <Route path="/users" element={<Users />} />
        <Route path="/channels" element={<Channels />} />
        <Route path="/accounts" element={<Accounts />} />
        <Route path="/settings" element={<Settings />} />
      </Route>

      {/* user 区 */}
      <Route element={<ProtectedRoute role="user"><AppLayout /></ProtectedRoute>}>
        <Route path="/portal" element={<PortalHome />} />
        <Route path="/portal/keys" element={<PortalKeys />} />
      </Route>

      {/* 兜底：进根路径时交给 ProtectedRoute 决定去向（登录页或按角色首页）。 */}
      <Route path="*" element={<ProtectedRoute><Navigate to="/overview" replace /></ProtectedRoute>} />
    </Routes>
  )
}
