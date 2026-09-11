import { useEffect, useState } from 'react'

/**
 * The colour theme.
 *
 * The app is dark by default because a simulation is looked at for long
 * stretches, but a light theme is offered for daylight and for printing a page
 * from the browser. The choice is remembered per browser; with none stored the
 * app follows the operating system's preference, so it opens the way the rest
 * of the machine looks.
 *
 * The data-view surfaces — the 3D and plan viewer, and the density canvases —
 * stay dark in both themes on purpose: their palettes are validated against a
 * dark ground, and a bright field of moving marks is harder to read on white.
 */
export type Theme = 'light' | 'dark'

const STORAGE_KEY = 'sim-theme'

function stored(): Theme | null {
  try {
    const value = localStorage.getItem(STORAGE_KEY)
    return value === 'light' || value === 'dark' ? value : null
  } catch {
    return null
  }
}

function systemPreference(): Theme {
  try {
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
  } catch {
    return 'dark'
  }
}

/** The theme in effect: an explicit choice if one was made, else the system's. */
export function resolvedTheme(): Theme {
  return stored() ?? systemPreference()
}

function apply(theme: Theme) {
  document.documentElement.setAttribute('data-theme', theme)
}

/**
 * Applies the theme before the app renders, so the first paint is already in
 * the right colours rather than flashing dark and correcting. Called from the
 * entry point.
 */
export function initTheme() {
  apply(resolvedTheme())
}

function setTheme(theme: Theme) {
  try {
    localStorage.setItem(STORAGE_KEY, theme)
  } catch {
    // A private window with storage blocked still themes for this session; the
    // choice just will not outlive the tab.
  }
  apply(theme)
}

/**
 * Drives the theme toggle. Returns the current theme and a function to flip it,
 * and keeps in step with the system preference until the user makes an explicit
 * choice.
 */
export function useTheme(): [Theme, () => void] {
  const [theme, setThemeState] = useState<Theme>(resolvedTheme)

  useEffect(() => {
    // While no explicit choice is stored, follow the system if it changes.
    if (stored()) return
    let media: MediaQueryList
    try {
      media = window.matchMedia('(prefers-color-scheme: light)')
    } catch {
      return
    }
    const onChange = () => {
      const next = systemPreference()
      apply(next)
      setThemeState(next)
    }
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])

  const toggle = () => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark'
    setTheme(next)
    setThemeState(next)
  }

  return [theme, toggle]
}
