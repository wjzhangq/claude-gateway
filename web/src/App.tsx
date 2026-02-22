import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { AuthProvider } from './context/AuthContext'
import { RequireAuth, RequireAdmin } from './components/RequireAuth'
import Layout from './components/Layout'
import LoginPage from './pages/LoginPage'
import DashboardPage from './pages/DashboardPage'
import APIKeysPage from './pages/APIKeysPage'
import UsagePage from './pages/UsagePage'
import ApplicationsPage from './pages/ApplicationsPage'
import AdminUsersPage from './pages/AdminUsersPage'
import AdminApplicationsPage from './pages/AdminApplicationsPage'
import AdminUsagePage from './pages/AdminUsagePage'

export default function App() {
  return (
    <AuthProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route element={<RequireAuth />}>
            <Route element={<Layout />}>
              <Route path="/dashboard" element={<DashboardPage />} />
              <Route path="/keys" element={<APIKeysPage />} />
              <Route path="/usage" element={<UsagePage />} />
              <Route path="/applications" element={<ApplicationsPage />} />
              <Route element={<RequireAdmin />}>
                <Route path="/admin/users" element={<AdminUsersPage />} />
                <Route path="/admin/applications" element={<AdminApplicationsPage />} />
                <Route path="/admin/usage" element={<AdminUsagePage />} />
              </Route>
            </Route>
          </Route>
          <Route path="*" element={<Navigate to="/dashboard" replace />} />
        </Routes>
      </BrowserRouter>
    </AuthProvider>
  )
}
