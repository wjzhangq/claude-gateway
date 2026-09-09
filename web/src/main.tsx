import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'

// Restore session cookie from localStorage on page load
function restoreSessionCookie() {
  const stored = localStorage.getItem('gateway_session_cookie')
  if (stored) {
    try {
      const { value, days } = JSON.parse(stored)
      const expires = new Date()
      expires.setTime(expires.getTime() + days * 24 * 60 * 60 * 1000)
      document.cookie = `gateway_session=${encodeURIComponent(value)}; path=/; expires=${expires.toUTCString()}; SameSite=Lax`
      console.log('Session cookie restored from localStorage')
    } catch (e) {
      console.error('Failed to restore session cookie:', e)
      localStorage.removeItem('gateway_session_cookie')
    }
  }
}

// Restore session before app starts
restoreSessionCookie()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
