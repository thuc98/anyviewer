import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
const routes=['/bootstrap','/session','/clipboard','/status','/frame.png','/connect','/disconnect','/event','/chat','/file','/settings'];
export default defineConfig({plugins:[react()],build:{outDir:'../backend/internal/webui/dist',emptyOutDir:true},server:{proxy:Object.fromEntries(routes.map(path=>[path,{target:'http://127.0.0.1:8765',changeOrigin:true,configure(proxy){proxy.on('proxyReq',req=>req.setHeader('Origin','http://127.0.0.1:8765'));}}]))}});
