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
import AWSPage from './pages/AWSPage'
import AWSKeysPage from './pages/AWSKeysPage'
import AWSUsagePage from './pages/AWSUsagePage'
import AdminAWSUsersPage from './pages/AdminAWSUsersPage'
import AdminAWSUsagePage from './pages/AdminAWSUsagePage'
import AdminAWSUserDailyPage from './pages/AdminAWSUserDailyPage'
import AdminAWSBedrockPage from './pages/AdminAWSBedrockPage'
import AdminPublicStatsPage from './pages/AdminPublicStatsPage'
import AdminPublicUsagePage from './pages/AdminPublicUsagePage'
import AdminQuotaPage from './pages/AdminQuotaPage'
import AdminPerfTestPage from './pages/AdminPerfTestPage'
import AdminInsightRankingPage from './pages/AdminInsightRankingPage'
import AdminInsightUserPage from './pages/AdminInsightUserPage'
import AdminInsightOrgPage from './pages/AdminInsightOrgPage'
import PlaygroundPage from './pages/PlaygroundPage'

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
              <Route path="/aws" element={<AWSPage />} />
              <Route path="/aws/keys" element={<AWSKeysPage />} />
              <Route path="/aws/usage" element={<AWSUsagePage />} />
              <Route element={<RequireAdmin />}>
                <Route path="/admin/users" element={<AdminUsersPage />} />
                <Route path="/admin/keys" element={<AdminKeysPage />} />
                <Route path="/admin/applications" element={<AdminApplicationsPage />} />
                <Route path="/admin/usage" element={<AdminUsagePage />} />
                <Route path="/admin/backends" element={<AdminBackendsPage />} />
                <Route path="/admin/public/stats" element={<AdminPublicStatsPage />} />
                <Route path="/admin/public/usage" element={<AdminPublicUsagePage />} />
                <Route path="/admin/user-daily" element={<AdminUserDailyPage />} />
                <Route path="/admin/quota" element={<AdminQuotaPage />} />
                <Route path="/admin/perftest" element={<AdminPerfTestPage />} />
                <Route path="/admin/insight/ranking" element={<AdminInsightRankingPage />} />
                <Route path="/admin/insight/user" element={<AdminInsightUserPage />} />
                <Route path="/admin/insight/user/:id" element={<AdminInsightUserPage />} />
                <Route path="/admin/insight/org" element={<AdminInsightOrgPage />} />
                <Route path="/admin/aws/users" element={<AdminAWSUsersPage />} />
                <Route path="/admin/aws/usage" element={<AdminAWSUsagePage />} />
                <Route path="/admin/aws/user-daily" element={<AdminAWSUserDailyPage />} />
                <Route path="/admin/aws/bedrock" element={<AdminAWSBedrockPage />} />
              </Route>
            </Route>
          </Route>
          <Route path="*" element={<Navigate to="/dashboard" replace />} />
        </Routes>
      </BrowserRouter>
    </AuthProvider>
  )
}
