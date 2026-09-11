import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { Project } from '@/api/types'

export function Projects() {
  const queryClient = useQueryClient()
  const [creating, setCreating] = useState(false)

  const projects = useQuery({
    queryKey: ['projects'],
    queryFn: () => api.projects.list(),
  })

  const create = useMutation({
    mutationFn: ({ name, description }: { name: string; description: string }) =>
      api.projects.create(name, description),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
      setCreating(false)
    },
  })

  return (
    <div className="page">
      <div className="page-narrow">
        <div className="page-header">
          <div>
            <h1>Projects</h1>
            <div className="sub">A project holds its own models, scenarios, runs and plans.</div>
          </div>
          <div className="spacer" />
          <button className="btn primary" onClick={() => setCreating(true)}>
            New project
          </button>
        </div>

        {creating && (
          <NewProjectForm
            busy={create.isPending}
            error={create.error instanceof Error ? create.error.message : null}
            onCancel={() => setCreating(false)}
            onSubmit={(name, description) => create.mutate({ name, description })}
          />
        )}

        {projects.isLoading && (
          <div className="loading-page" style={{ height: 200 }}>
            <span className="spinner" />
          </div>
        )}

        {projects.error && (
          <div className="banner error">
            {projects.error instanceof Error ? projects.error.message : 'Could not load your projects.'}
          </div>
        )}

        {projects.data?.length === 0 && !creating && (
          <div className="empty">
            <h2>No projects yet</h2>
            <p>Create one to start building models and running scenarios.</p>
            <button className="btn primary" onClick={() => setCreating(true)}>
              New project
            </button>
          </div>
        )}

        {projects.data && projects.data.length > 0 && (
          <div className="grid">
            {projects.data.map((project) => (
              <ProjectCard key={project.id} project={project} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function ProjectCard({ project }: { project: Project }) {
  return (
    <Link to={`/projects/${project.id}`} className="card" style={{ display: 'block', color: 'inherit' }}>
      <div className="row" style={{ marginBottom: 8 }}>
        <h2 style={{ flex: 1 }}>{project.name}</h2>
        {project.role && <span className="pill">{project.role}</span>}
      </div>

      <p className="dim" style={{ margin: '0 0 12px', fontSize: 13, minHeight: 20 }}>
        {project.description || 'No description.'}
      </p>

      <div className="faint" style={{ fontSize: 12 }}>
        Updated {formatDate(project.updatedAt)}
      </div>
    </Link>
  )
}

function NewProjectForm({
  busy,
  error,
  onSubmit,
  onCancel,
}: {
  busy: boolean
  error: string | null
  onSubmit: (name: string, description: string) => void
  onCancel: () => void
}) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (name.trim()) onSubmit(name.trim(), description.trim())
  }

  return (
    <form className="card" onSubmit={submit} style={{ marginBottom: 16 }}>
      <h2 style={{ marginBottom: 14 }}>New project</h2>

      {error && <div className="banner error">{error}</div>}

      <div className="field">
        <label htmlFor="project-name">Name</label>
        <input
          id="project-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Rotterdam terminal"
          autoFocus
          required
        />
      </div>

      <div className="field">
        <label htmlFor="project-description">Description</label>
        <textarea
          id="project-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={2}
          placeholder="What this project is for."
        />
      </div>

      <div className="row">
        <button className="btn primary" type="submit" disabled={busy || !name.trim()}>
          {busy && <span className="spinner" />}
          Create
        </button>
        <button className="btn ghost" type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  )
}

export function formatDate(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'

  const elapsed = Date.now() - date.getTime()
  const minutes = Math.round(elapsed / 60_000)

  // Relative for anything recent, because "3 minutes ago" answers the question
  // a reader actually has about a run that may still be going.
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} min ago`

  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} h ago`

  const days = Math.round(hours / 24)
  if (days < 7) return `${days} d ago`

  return date.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' })
}
