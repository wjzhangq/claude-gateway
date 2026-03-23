import { useState, useEffect, useCallback } from 'react'

interface ToastMessage {
  id: number
  text: string
}

let addToast: (msg: string) => void = () => {}

export function toast(msg: string) {
  addToast(msg)
}

let nextId = 0

export default function ToastContainer() {
  const [messages, setMessages] = useState<ToastMessage[]>([])

  const add = useCallback((text: string) => {
    const id = nextId++
    setMessages((prev) => [...prev, { id, text }])
    setTimeout(() => {
      setMessages((prev) => prev.filter((m) => m.id !== id))
    }, 5000)
  }, [])

  useEffect(() => {
    addToast = add
    return () => { addToast = () => {} }
  }, [add])

  if (messages.length === 0) return null

  return (
    <div className="fixed top-4 right-4 z-50 flex flex-col gap-2 max-w-sm">
      {messages.map((m) => (
        <div
          key={m.id}
          className="bg-red-600 text-white text-sm px-4 py-3 rounded-xl shadow-lg animate-[slideIn_0.2s_ease-out] flex items-start gap-2"
        >
          <span className="flex-shrink-0 mt-0.5">&#9888;</span>
          <span>{m.text}</span>
          <button
            onClick={() => setMessages((prev) => prev.filter((msg) => msg.id !== m.id))}
            className="ml-auto flex-shrink-0 text-white/70 hover:text-white"
          >
            &#10005;
          </button>
        </div>
      ))}
    </div>
  )
}
