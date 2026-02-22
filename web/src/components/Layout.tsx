import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useAuth } from '../context/AuthContext'
import { logout } from '../api'

const userNav = [
  { to: '/dashboard', label: '仪表盘' },
  { to: '/keys', label: 'API Keys' },
  { to: '/usage', label: '使用统计' },
  { to: '/applications', label: '申请记录' },
]

const adminNav = [
  { to: '/admin/users', label: '用户管理' },
  { to: '/admin/applications', label: '审批管理' },
  { to: '/admin/usage', label: '使用统计' },
  { to: '/admin/backends', label: 'Backend 统计' },
]

export default function Layout() {
  const { user, isAdmin, setUser } = useAuth()
  const navigate = useNavigate()

  const handleLogout = async () => {
    await logout().catch(() => {})
    setUser(null)
    navigate('/login')
  }

  return (
    <div className="flex h-screen bg-[#f4f6f9]">
      {/* Sidebar */}
      <aside className="w-60 bg-white border-r border-gray-100 flex flex-col shadow-sm">
        {/* Logo */}
        <div className="px-5 py-5 border-b border-gray-100">
          <div className="flex items-center gap-2.5">
            <div className="w-7 h-7 bg-red-600 rounded-lg flex items-center justify-center flex-shrink-0">
              <span className="text-white text-xs font-bold">CG</span>
            </div>
            <span className="text-sm font-semibold text-gray-900">Claude Gateway</span>
          </div>
        </div>

        {/* Nav */}
        <nav className="flex-1 px-3 py-4 overflow-y-auto">
          <p className="px-3 pb-2 text-[10px] font-semibold text-gray-400 uppercase tracking-widest">工作台</p>
          <div className="space-y-0.5">
            {userNav.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                className={({ isActive }) =>
                  `flex items-center px-3 py-2 rounded-lg text-sm font-medium transition-all ${
                    isActive
                      ? 'bg-red-50 text-red-700 border-l-2 border-red-600 pl-[10px]'
                      : 'text-gray-500 hover:bg-gray-50 hover:text-gray-800'
                  }`
                }
              >
                {item.label}
              </NavLink>
            ))}
          </div>

          {isAdmin && (
            <>
              <p className="px-3 pt-5 pb-2 text-[10px] font-semibold text-gray-400 uppercase tracking-widest">管理员</p>
              <div className="space-y-0.5">
                {adminNav.map((item) => (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    className={({ isActive }) =>
                      `flex items-center px-3 py-2 rounded-lg text-sm font-medium transition-all ${
                        isActive
                          ? 'bg-red-50 text-red-700 border-l-2 border-red-600 pl-[10px]'
                          : 'text-gray-500 hover:bg-gray-50 hover:text-gray-800'
                      }`
                    }
                  >
                    {item.label}
                  </NavLink>
                ))}
              </div>
            </>
          )}
        </nav>

        {/* User footer */}
        <div className="px-4 py-4 border-t border-gray-100 bg-gray-50/60">
          <div className="flex items-center gap-2.5 mb-2.5">
            <div className="w-7 h-7 rounded-full bg-red-100 flex items-center justify-center flex-shrink-0">
              <span className="text-xs font-semibold text-red-700">
                {user?.itcode?.slice(0, 1).toUpperCase() || 'U'}
              </span>
            </div>
            <div className="min-w-0">
              <p className="text-xs font-medium text-gray-800 truncate">{user?.itcode}</p>
              <p className="text-[10px] text-gray-400">{isAdmin ? '管理员' : '普通用户'}</p>
            </div>
          </div>
          <button
            onClick={handleLogout}
            className="w-full text-xs text-gray-400 hover:text-red-600 text-left transition-colors"
          >
            退出登录 →
          </button>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto">
        <Outlet />
      </main>
    </div>
  )
}
