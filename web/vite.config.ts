import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [react()],

  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },

  server: {
    port: 5173,
    // During development the SPA runs on Vite and the API on the Go server.
    // Proxying rather than enabling CORS keeps the session cookie same-origin,
    // which is what it is in production behind IIS.
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: false,
      },
    },
  },

  build: {
    outDir: 'dist',
    sourcemap: true,
    rollupOptions: {
      output: {
        // three.js is by far the largest dependency and changes rarely, so it
        // gets its own chunk and stays cached across deploys.
        manualChunks: {
          three: ['three'],
          vendor: ['react', 'react-dom', 'react-router-dom', '@tanstack/react-query'],
        },
      },
    },
  },
})
