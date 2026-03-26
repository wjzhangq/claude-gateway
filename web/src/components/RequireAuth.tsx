import { Navigate, Outlet } from 'react-router-dom'
import { useAuth } from '../context/AuthContext'

export function RequireAuth() {
  const { user } = useAuth()
  if (!user) return <Navigate to="/login" replace />
  // pending/disabled users go to the activation request page
  if (user.status !== 'active' && user.role !== 'admin') {
    return <Navigate to="/pending" replace />
  }
  return <Outlet />
}

export function RequireAdmin() {
  const { user, isAdmin } = useAuth()
  if (!user) return <Navigate to="/login" replace />
  if (!isAdmin) return <Navigate to="/dashboard" replace />
  return <Outlet />
}
