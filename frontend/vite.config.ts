import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';
import tailwind from '@tailwindcss/vite';

// The Go application owns the API, SSE events and static production assets.
// During development Vite proxies the public application routes to it.
const proxy={target:process.env.RKA_BACKEND_URL||'http://127.0.0.1:8080',changeOrigin:false};

export default defineConfig({
  plugins:[react(),tailwind()],
  server:{port:5173,strictPort:true,proxy:{'/api':proxy,'/healthz':proxy,'/readyz':proxy,'/content':proxy}},
  preview:{port:4173,strictPort:true,proxy:{'/api':proxy,'/healthz':proxy,'/readyz':proxy,'/content':proxy}},
});
