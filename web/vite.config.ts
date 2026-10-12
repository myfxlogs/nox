import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/arb.dashboard.v1.DashboardService": "http://localhost:50051",
    },
  },
  build: {
    emptyOutDir: false,
  },
});
