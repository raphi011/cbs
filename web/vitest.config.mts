import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// Pure-function tests only (node env, no DOM). The alias mirrors tsconfig's
// "@/*" → "./src/*" so test files import the same way app code does.
export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
});
