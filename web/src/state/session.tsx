import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'

import { ApiError, api } from '@/api/client'
import type { AuthConfig, User } from '@/api/types'

interface SessionValue {
  user: User | null
  config: AuthConfig | null
  /** True until the first attempt to resolve the cookie has finished. Routing
   *  on an unresolved session would flash the sign-in page at a signed-in user. */
  loading: boolean

  signIn: (email: string, password: string) => Promise<void>
  register: (email: string, password: string, displayName: string) => Promise<void>
  signOut: () => Promise<void>
  refresh: () => Promise<void>
}

const SessionContext = createContext<SessionValue | null>(null)

export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [config, setConfig] = useState<AuthConfig | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const session = await api.auth.me()
      setUser(session.user)
    } catch (err) {
      // A 401 here is the normal state for a visitor, not a failure.
      if (!(err instanceof ApiError && err.isUnauthorized)) {
        console.warn('Could not resolve the session', err)
      }
      setUser(null)
    }
  }, [])

  useEffect(() => {
    let cancelled = false

    const boot = async () => {
      // The sign-in page needs the server's auth settings before anyone has
      // signed in, so both are fetched together rather than in sequence.
      const [configResult] = await Promise.allSettled([api.auth.config(), refresh()])

      if (cancelled) return
      if (configResult.status === 'fulfilled') setConfig(configResult.value)
      setLoading(false)
    }

    void boot()
    return () => {
      cancelled = true
    }
  }, [refresh])

  const value = useMemo<SessionValue>(
    () => ({
      user,
      config,
      loading,

      signIn: async (email, password) => {
        const session = await api.auth.login(email, password)
        setUser(session.user)
      },

      register: async (email, password, displayName) => {
        const session = await api.auth.register(email, password, displayName)
        setUser(session.user)
      },

      signOut: async () => {
        await api.auth.logout()
        setUser(null)
      },

      refresh,
    }),
    [user, config, loading, refresh],
  )

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}

export function useSession(): SessionValue {
  const value = useContext(SessionContext)
  if (!value) throw new Error('useSession must be used inside a SessionProvider.')
  return value
}
