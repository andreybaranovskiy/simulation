import { useState, type FormEvent } from 'react'

import { ApiError } from '@/api/client'
import { useSession } from '@/state/session'

export function SignIn() {
  const { config, signIn, register } = useSession()

  const [mode, setMode] = useState<'signin' | 'register'>('signin')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')

  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [fields, setFields] = useState<Record<string, string>>({})

  const registering = mode === 'register'
  const canRegister = config?.allowRegistration ?? false

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setBusy(true)
    setError(null)
    setFields({})

    try {
      if (registering) {
        await register(email, password, displayName)
      } else {
        await signIn(email, password)
      }
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message)
        // Per-field problems go next to the input that caused them; a wall of
        // text at the top makes the user hunt for which box is wrong.
        setFields(err.fields ?? {})
      } else {
        setError('Could not reach the server. Check that it is running.')
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="auth-page">
      <form className="card auth-card" onSubmit={submit}>
        <div className="card-head">
          <span className="mark" style={{ width: 26, height: 26, borderRadius: 7 }} />
          <div>
            <h1>{registering ? 'Create an account' : 'Sign in'}</h1>
            <div className="sub dim" style={{ fontSize: 13 }}>
              Simulation Platform
            </div>
          </div>
        </div>

        {error && <div className="banner error">{error}</div>}

        {registering && (
          <div className="field">
            <label htmlFor="displayName">Your name</label>
            <input
              id="displayName"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              autoComplete="name"
            />
            {fields.displayName && <div className="error">{fields.displayName}</div>}
          </div>
        )}

        <div className="field">
          <label htmlFor="email">Email</label>
          <input
            id="email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="username"
            required
          />
          {fields.email && <div className="error">{fields.email}</div>}
        </div>

        <div className="field">
          <label htmlFor="password">Password</label>
          <input
            id="password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={registering ? 'new-password' : 'current-password'}
            required
          />
          {fields.password && <div className="error">{fields.password}</div>}
          {registering && !fields.password && config && (
            <div className="hint">
              At least {config.minPasswordLength} characters, mixing letters with digits or symbols.
            </div>
          )}
        </div>

        <button className="btn primary" type="submit" disabled={busy} style={{ width: '100%' }}>
          {busy && <span className="spinner" />}
          {registering ? 'Create account' : 'Sign in'}
        </button>

        {canRegister && (
          <div style={{ marginTop: 14, textAlign: 'center', fontSize: 13 }}>
            <button
              type="button"
              className="btn ghost small"
              onClick={() => {
                setMode(registering ? 'signin' : 'register')
                setError(null)
                setFields({})
              }}
            >
              {registering ? 'I already have an account' : 'Create an account'}
            </button>
          </div>
        )}

        {!canRegister && !registering && (
          <div className="hint" style={{ marginTop: 14, textAlign: 'center' }}>
            Registration is closed on this server. Ask an administrator for an account.
          </div>
        )}
      </form>
    </div>
  )
}
