import { Navigate, NavLink, Route, Routes, useParams } from 'react-router-dom'

import { SessionProvider, useSession } from '@/state/session'
import { SignIn } from '@/pages/SignIn'
import { Projects } from '@/pages/Projects'
import { ProjectPage } from '@/pages/ProjectPage'
import { PlanCalibrate } from '@/pages/PlanCalibrate'
import { RunViewer } from '@/pages/RunViewer'

export function App() {
  return (
    <SessionProvider>
      <Shell />
    </SessionProvider>
  )
}

function Shell() {
  const { user, loading } = useSession()

  if (loading) {
    return (
      <div className="loading-page">
        <span className="spinner" />
        <span>Loading</span>
      </div>
    )
  }

  if (!user) {
    return (
      <Routes>
        <Route path="*" element={<SignIn />} />
      </Routes>
    )
  }

  return (
    <div className="app">
      <TopBar />
      <Routes>
        <Route path="/" element={<Navigate to="/projects" replace />} />
        <Route path="/projects" element={<Projects />} />
        <Route path="/projects/:projectId" element={<ProjectPage />} />
        <Route path="/projects/:projectId/plans/:planId" element={<PlanCalibrate />} />
        {/* The viewer is full-bleed and manages its own scrolling. */}
        <Route path="/projects/:projectId/runs/:runId" element={<RunViewer />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
    </div>
  )
}

function TopBar() {
  const { user, signOut } = useSession()
  const { projectId } = useParams()

  return (
    <header className="topbar">
      <NavLink to="/projects" className="brand">
        <span className="mark" />
        Simulation
      </NavLink>

      <nav>
        <NavLink to="/projects" className={({ isActive }) => (isActive ? 'active' : '')}>
          Projects
        </NavLink>
        {projectId && (
          <NavLink
            to={`/projects/${projectId}`}
            className={({ isActive }) => (isActive ? 'active' : '')}
          >
            Scenarios
          </NavLink>
        )}
      </nav>

      <div className="spacer" />

      <span className="dim" style={{ fontSize: 13 }}>
        {user?.displayName}
        {user?.isAdmin && (
          <span className="pill" style={{ marginLeft: 8 }}>
            Admin
          </span>
        )}
      </span>

      <button className="btn ghost small" onClick={() => void signOut()}>
        Sign out
      </button>
    </header>
  )
}

function NotFound() {
  return (
    <div className="page">
      <div className="empty">
        <h2>That page does not exist</h2>
        <p>
          <NavLink to="/projects">Back to your projects</NavLink>
        </p>
      </div>
    </div>
  )
}
