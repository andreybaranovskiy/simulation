import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { App } from './App'
import { ApiError } from './api/client'
import './styles/app.css'
import './styles/charts.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // A run's artifacts never change once it is done, and project lists
      // change rarely. Refetching on every window focus is noise.
      refetchOnWindowFocus: false,
      staleTime: 15_000,

      retry: (failureCount, error) => {
        // Retrying a rejected request cannot help and only delays the message
        // the user needs to see.
        if (error instanceof ApiError && error.status < 500) return false
        return failureCount < 2
      },
    },
  },
})

const container = document.getElementById('root')
if (!container) throw new Error('The page has no root element.')

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
