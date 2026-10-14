import { defineConfig } from "vitest/config";

// The /v1 control-plane modules are pure TS over WebCrypto globals (available in
// Node >= 20), so they run in the default node environment — no Workers pool
// needed. DO/Queue-backed wiring is integration-tested separately.
export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    exclude: ["**/node_modules/**"],
  },
});
