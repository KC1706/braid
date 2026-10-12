import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    // Never pick up the throwaway repos the demo/e2e tests generate.
    exclude: ["**/node_modules/**", ".tmp/**"],
  },
});
