import { Navigate, Outlet } from 'react-router-dom'
import { useAuth } from '../context/AuthContext'

export function RequireAuth() {
  const { user } = useAuth()
  if (!user) return <Navigate to="/login" replace />
  return <Outlet />
}

export function RequireAdmin() {
  const { user, isAdmin } = useAuth()
  if (!user) return <Navigate to="/login" replace />
  if (!isAdmin) return <Navigate to="/dashboard" replace />
  return <Outlet />
}
