import { createContext, useContext, useState } from 'react'
import type { ReactNode } from 'react'

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
