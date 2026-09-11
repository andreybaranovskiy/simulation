import { Navigate, NavLink, Route, Routes, useLocation } from 'react-router-dom'

import { SessionProvider, useSession } from '@/state/session'
import { SignIn } from '@/pages/SignIn'
import { Projects } from '@/pages/Projects'
import { ProjectPage } from '@/pages/ProjectPage'
import { PlanCalibrate } from '@/pages/PlanCalibrate'
import { RunViewer } from '@/pages/RunViewer'
import { Compare } from '@/pages/Compare'
import { Reports } from '@/pages/Reports'
import { PlanEditor } from '@/pages/PlanEditor'
import { ReportPrint } from '@/print/ReportPrint'
import { useTheme } from '@/state/theme'

export function App() {
  return (
    <SessionProvider>
      <Shell />
    </SessionProvider>
  )
}

function Shell() {
  const { user, loading } = useSession()
  const { pathname } = useLocation()

  // Print routes are loaded by the PDF renderer with a session cookie but no
  // interactive session, so they render the document straight away: no app
  // chrome, no loading screen, no sign-in gate to wait behind.
  if (pathname.startsWith('/print/')) {
    return (
      <Routes>
        <Route path="/print/projects/:projectId/reports/:reportId" element={<ReportPrint />} />
        <Route path="/print/projects/:projectId/report" element={<ReportPrint />} />
      </Routes>
    )
  }

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
        <Route path="/projects/:projectId/compare" element={<Compare />} />
        <Route path="/projects/:projectId/reports" element={<Reports />} />
        <Route path="/projects/:projectId/models/:modelId/layout" element={<PlanEditor />} />
        {/* The viewer is full-bleed and manages its own scrolling. */}
        <Route path="/projects/:projectId/runs/:runId" element={<RunViewer />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
    </div>
  )
}

function TopBar() {
  const { user, signOut } = useSession()

  // The bar sits outside the route tree, so it has no route params of its own
  // and has to read the project out of the path.
  const { pathname } = useLocation()
  const projectId = pathname.match(/^\/projects\/([^/]+)/)?.[1]

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
          <>
            <NavLink
              to={`/projects/${projectId}`}
              end
              className={({ isActive }) => (isActive ? 'active' : '')}
            >
              Scenarios
            </NavLink>
            <NavLink
              to={`/projects/${projectId}/compare`}
              className={({ isActive }) => (isActive ? 'active' : '')}
            >
              Compare
            </NavLink>
            <NavLink
              to={`/projects/${projectId}/reports`}
              className={({ isActive }) => (isActive ? 'active' : '')}
            >
              Reports
            </NavLink>
          </>
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

      <ThemeToggle />

      <button className="btn ghost small" onClick={() => void signOut()}>
        Sign out
      </button>
    </header>
  )
}

function ThemeToggle() {
  const [theme, toggle] = useTheme()
  const dark = theme === 'dark'
  return (
    <button
      className="btn ghost small icon-btn"
      onClick={toggle}
      title={dark ? 'Switch to the light theme' : 'Switch to the dark theme'}
      aria-label={dark ? 'Switch to the light theme' : 'Switch to the dark theme'}
    >
      {dark ? '☀' : '☽'}
    </button>
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
