import axios from 'axios'
import { toast } from './components/Toast'

const api = axios.create({
  baseURL: '/',
  withCredentials: true,
  timeout: 15000,
})

api.interceptors.response.use(
  (res) => res,
  (err) => {
    if (err.response?.status === 401) {
      window.location.href = '/login'
    } else if (err.response) {
      const msg = err.response.data?.error || `请求失败 (HTTP ${err.response.status})`
      toast(msg)
    } else if (err.request) {
      toast('网络连接失败，请检查后端服务是否运行')
    } else {
      toast('请求异常: ' + err.message)
    }
    return Promise.reject(err)
  }
)

export default api

// Auth
export const sendCode = (itcode: string, inviteCode?: string) =>
  api.post("/api/auth/send-code", { itcode, invite_code: inviteCode || undefined })

export const login = (itcode: string, code: string, inviteCode?: string) =>
  api.post("/api/auth/login", { itcode, code, invite_code: inviteCode || undefined })

export const logout = () => api.post('/api/auth/logout')
export const getMe = () => api.get('/api/me')

// API Keys
export const listKeys = () => api.get('/api/keys')
export const createKey = (name: string) =>
  api.post('/api/keys', { name })
export const disableKey = (id: number) => api.put(`/api/keys/${id}/disable`)
export const enableKey = (id: number) => api.put(`/api/keys/${id}/enable`)
export const setAutoDowngrade = (id: number, autoDowngrade: boolean) =>
  api.put(`/api/keys/${id}/auto-downgrade`, { auto_downgrade: autoDowngrade })
export const renameKey = (id: number, name: string) =>
  api.put(`/api/keys/${id}/rename`, { name })
export const deleteKey = (id: number) => api.delete(`/api/keys/${id}`)
export const switchKeyChannel = (id: number, channel: 'backend' | 'aws') =>
  api.put(`/api/keys/${id}/channel`, { channel })

// Usage
export const getMyUsage = (params?: Record<string, string | number>) =>
  api.get('/api/usage', { params })
export const getMyDailyStats = (params?: Record<string, string | number>) =>
  api.get('/api/usage/daily', { params })

// Applications
export const submitApplication = (reason: string) =>
  api.post('/api/applications', { reason })
export const listMyApplications = (status?: string) =>
  api.get('/api/applications', { params: status ? { status } : {} })

// Admin - Users
export const adminListUsers = (params?: Record<string, string | number>) =>
  api.get('/admin/api/users', { params })
export const adminSearchUsers = (q: string, limit = 10) =>
  api.get('/admin/api/users/search', { params: { q, limit } })
export const adminGetUser = (id: number) => api.get(`/admin/api/users/${id}`)
export const adminCreateUser = (data: Record<string, unknown>) =>
  api.post('/admin/api/users', data)
export const adminUpdateUser = (id: number, data: Record<string, unknown>) =>
  api.put(`/admin/api/users/${id}`, data)
export const adminUpdateItcode = (id: number, itcode: string) =>
  api.put(`/admin/api/users/${id}/itcode`, { itcode })

// Admin - Keys
export const adminListKeys = (params?: Record<string, string | number>) =>
  api.get('/admin/api/keys', { params })
export const adminCreateKey = (data: { user_id: number; name?: string; channel?: string }) =>
  api.post('/admin/api/keys', data)
export const adminRenameKey = (id: number, name: string) =>
  api.put(`/admin/api/keys/${id}/rename`, { name })
export const adminSwitchKeyChannel = (id: number, channel: 'backend' | 'aws') =>
  api.put(`/admin/api/keys/${id}/channel`, { channel })
export const adminTransferKey = (id: number, itcode: string) =>
  api.put(`/admin/api/keys/${id}/transfer`, { itcode })

// Admin - Usage
export const adminGetUsage = (params?: Record<string, string | number>) =>
  api.get('/admin/api/usage', { params })
export const adminGetDailyStats = (params?: Record<string, string | number>) =>
  api.get('/admin/api/usage/daily', { params })

export const adminGetUserDailyCost = (params?: Record<string, string | number>) =>
  api.get('/admin/api/usage/user-daily', { params })

// Admin - Applications
export const adminListApplications = (status?: string) =>
  api.get('/admin/api/applications', { params: status ? { status } : {} })
export const adminReviewApplication = (
  id: number,
  status: 'approved' | 'rejected',
  note?: string,
  groupId?: number
) => api.put(`/admin/api/applications/${id}/review`, { status, note, group_id: groupId })

// Admin - Backends
export const adminGetBackendStats = (params?: Record<string, string>) =>
  api.get('/admin/api/backends/stats', { params })
export const adminGetBackendStatus = () => api.get('/admin/api/backends/status')

// Admin - Groups
export const adminGetGroups = () => api.get('/admin/api/groups')

// ===== User AWS =====
export const getAWSDashboard = () => api.get('/api/aws/dashboard')
export const listAWSKeys = () => api.get('/api/aws/keys')
export const createAWSKey = (name: string) => api.post('/api/aws/keys', { name })
export const getAWSMyUsage = (params?: Record<string, string | number>) =>
  api.get('/api/aws/usage', { params })
export const getAWSMyDailyStats = (params?: Record<string, string | number>) =>
  api.get('/api/aws/usage/daily', { params })

// ===== Admin AWS =====
export const adminListAWSUsers = (params?: Record<string, string | number>) =>
  api.get('/admin/api/aws/users', { params })
export const adminToggleAWSUser = (id: number) =>
  api.put(`/admin/api/aws/users/${id}/toggle`)
export const adminEnableAWSByItcode = (itcode: string) =>
  api.post('/admin/api/aws/users/enable', { itcode })
export const adminGetAWSUsage = (params?: Record<string, string | number>) =>
  api.get('/admin/api/aws/usage', { params })
export const adminGetAWSDailyStats = (params?: Record<string, string | number>) =>
  api.get('/admin/api/aws/usage/daily', { params })
export const adminGetAWSUserDailyCost = (params?: Record<string, string | number>) =>
  api.get('/admin/api/aws/usage/user-daily', { params })
export const adminGetAWSKeys = (params?: Record<string, string | number>) =>
  api.get('/admin/api/aws/keys', { params })
export const adminGetAWSBedrockStats = (params?: Record<string, string>) =>
  api.get('/admin/api/aws/bedrock/stats', { params })
