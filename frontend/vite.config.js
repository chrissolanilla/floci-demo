import { fileURLToPath } from 'node:url';
import { defineConfig, loadEnv } from 'vite';
import vue from '@vitejs/plugin-vue';

// Vite and Go both read the single .env in the repository root.
const envDir = fileURLToPath(new URL('..', import.meta.url));

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, envDir, '');
  const port = Number(env.FRONTEND_PORT || 5173);

  return {
    plugins: [vue()],
    envDir,
    server: {
      port,
      strictPort: true,
      // Keep requests same-origin, including cookies and Origin/Host headers.
      proxy: {
        '/api': {
          target: `http://localhost:${env.BACKEND_PORT || 8080}`,
          changeOrigin: false,
        },
      },
    },
    preview: { port, strictPort: true },
  };
});
