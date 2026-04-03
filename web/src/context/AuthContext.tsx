import { createContext, useContext, useState, useEffect } from 'react'
import type { ReactNode } from 'react'
import { getMe } from '../api'

interface AuthUser {
  id: number
  itcode: string
  role: string
  status: string
  aws_enabled: boolean
}

interface AuthContextType {
  user: AuthUser | null
  setUser: (u: AuthUser | null) => void
  isAdmin: boolean
  isActive: boolean
  isAWSEnabled: boolean
}

const AuthContext = createContext<AuthContextType>({
  user: null,
  setUser: () => {},
  isAdmin: false,
  isActive: false,
  isAWSEnabled: false,
})

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(() => {
    try {
      const s = sessionStorage.getItem('user')
      return s ? JSON.parse(s) : null
    } catch {
      return null
    }
  })

  const handleSetUser = (u: AuthUser | null) => {
    setUser(u)
    if (u) sessionStorage.setItem('user', JSON.stringify(u))
    else sessionStorage.removeItem('user')
  }

  // Refresh user info from server on mount so changes (e.g. aws_enabled) are reflected
  // without requiring a re-login.
  useEffect(() => {
    if (!sessionStorage.getItem('user')) return
    getMe()
      .then((res) => {
        const fresh = res.data.user
        if (fresh) handleSetUser(fresh)
      })
      .catch(() => {
        // 401 means session expired — clear local state
        handleSetUser(null)
      })
  }, [])

  return (
    <AuthContext.Provider
      value={{
        user,
        setUser: handleSetUser,
        isAdmin: user?.role === 'admin',
        isActive: user?.status === 'active',
        isAWSEnabled: user?.aws_enabled ?? false,
      }}
    >
      {children}
    </AuthContext.Provider>
  )
}

export const useAuth = () => useContext(AuthContext)
