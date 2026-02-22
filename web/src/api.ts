import axios from 'axios'

const api = axios.create({
  baseURL: '/',
  withCredentials: true,
})

api.interceptors.response.use(
  (res) => res,
  (err) => {
    if (err.response?.status === 401) {
      window.location.href = '/login'
    }
    return Promise.reject(err)
  }
)

export default api

// Auth
export const sendCode = (phone: string) =>
  api.post('/api/auth/send-code', { phone })

export const login = (phone: string, code: string) =>
  api.post('/api/auth/login', { phone, code })

export const logout = () => api.post('/api/auth/logout')

// API Keys
export const listKeys = () => api.get('/api/keys')
export const createKey = (name: string, expiresAt?: string) =>
  api.post('/api/keys', { name, expires_at: expiresAt })
export const disableKey = (id: number) => api.put(`/api/keys/${id}/disable`)
export const enableKey = (id: number) => api.put(`/api/keys/${id}/enable`)
export const deleteKey = (id: number) => api.delete(`/api/keys/${id}`)

// Usage
export const getMyUsage = (params?: Record<string, string | number>) =>
  api.get('/api/usage', { params })

// Applications
export const submitApplication = (model: string, reason: string) =>
  api.post('/api/applications', { model, reason })
export const listMyApplications = (status?: string) =>
  api.get('/api/applications', { params: status ? { status } : {} })

// Admin - Users
export const adminListUsers = () => api.get('/admin/api/users')
export const adminGetUser = (id: number) => api.get(`/admin/api/users/${id}`)
export const adminCreateUser = (data: Record<string, unknown>) =>
  api.post('/admin/api/users', data)
export const adminUpdateUser = (id: number, data: Record<string, unknown>) =>
  api.put(`/admin/api/users/${id}`, data)

// Admin - Usage
export const adminGetUsage = (params?: Record<string, string | number>) =>
  api.get('/admin/api/usage', { params })
export const adminGetDailyStats = (params?: Record<string, string | number>) =>
  api.get('/admin/api/usage/daily', { params })

// Admin - Applications
export const adminListApplications = (status?: string) =>
  api.get('/admin/api/applications', { params: status ? { status } : {} })
export const adminReviewApplication = (
  id: number,
  status: 'approved' | 'rejected',
  note?: string
) => api.put(`/admin/api/applications/${id}/review`, { status, note })
