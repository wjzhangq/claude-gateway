import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { AuthProvider } from './context/AuthContext'
import { RequireAuth, RequireAdmin } from './components/RequireAuth'
import ToastContainer from './components/Toast'
import Layout from './components/Layout'
import LoginPage from './pages/LoginPage'
import PendingPage from './pages/PendingPage'
import DashboardPage from './pages/DashboardPage'
import APIKeysPage from './pages/APIKeysPage'
import UsagePage from './pages/UsagePage'
import AdminUsersPage from './pages/AdminUsersPage'
import AdminKeysPage from './pages/AdminKeysPage'
import AdminApplicationsPage from './pages/AdminApplicationsPage'
import AdminUsagePage from './pages/AdminUsagePage'
import AdminBackendsPage from './pages/AdminBackendsPage'
import AdminUserDailyPage from './pages/AdminUserDailyPage'
import AdminPublicStatsPage from './pages/AdminPublicStatsPage'
import AdminPublicUsagePage from './pages/AdminPublicUsagePage'
import AdminProviderStatsPage from './pages/AdminProviderStatsPage'
import AdminDashboardPage from './pages/AdminDashboardPage'
import PlaygroundPage from './pages/PlaygroundPage'
import ApplicationsPage from './pages/ApplicationsPage'

export default function App() {
  return (
    <AuthProvider>
      <ToastContainer />
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/pending" element={<PendingPage />} />
          <Route element={<RequireAuth />}>
            <Route element={<Layout />}>
              <Route path="/dashboard" element={<DashboardPage />} />
              <Route path="/keys" element={<APIKeysPage />} />
              <Route path="/usage" element={<UsagePage />} />
              <Route path="/playground" element={<PlaygroundPage />} />
              <Route path="/applications" element={<ApplicationsPage />} />
              <Route element={<RequireAdmin />}>
                <Route path="/admin/dashboard" element={<AdminDashboardPage />} />
                <Route path="/admin/users" element={<AdminUsersPage />} />
                <Route path="/admin/keys" element={<AdminKeysPage />} />
                <Route path="/admin/applications" element={<AdminApplicationsPage />} />
                <Route path="/admin/usage" element={<AdminUsagePage />} />
                <Route path="/admin/backends" element={<AdminBackendsPage />} />
                <Route path="/admin/public/stats" element={<AdminPublicStatsPage />} />
                <Route path="/admin/public/usage" element={<AdminPublicUsagePage />} />
                <Route path="/admin/user-daily" element={<AdminUserDailyPage />} />
                <Route path="/admin/provider/backend" element={<AdminProviderStatsPage provider="backend" />} />
                <Route path="/admin/provider/aws" element={<AdminProviderStatsPage provider="aws" />} />
                <Route path="/admin/provider/kimi" element={<AdminProviderStatsPage provider="kimi" />} />
                <Route path="/admin/provider/minimax" element={<AdminProviderStatsPage provider="minimax" />} />
              </Route>
            </Route>
          </Route>
          <Route path="*" element={<Navigate to="/dashboard" replace />} />
        </Routes>
      </BrowserRouter>
    </AuthProvider>
  )
}
