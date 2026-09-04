import { defineConfig } from "@playwright/test";

const chromiumExecutable = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;

export default defineConfig({
  testDir: "./e2e",
  timeout: 45_000,
  workers: 1,
  fullyParallel: false,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:4174",
    browserName: "chromium",
    launchOptions: chromiumExecutable ? { executablePath: chromiumExecutable } : undefined,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "npm run build && cd ../server && PORT=4174 ALLOW_GUESTS=true CLIENT_DIST=../client/dist DB_PATH=/private/tmp/kabo-responsive.sqlite GOCACHE=/private/tmp/kabo-responsive-go-cache go run .",
    url: "http://127.0.0.1:4174/healthz",
    timeout: 120_000,
    reuseExistingServer: false,
  },
});
