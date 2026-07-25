import { expect, test } from "@playwright/test";

const now = "2026-07-25T00:00:00Z";
const host = {
  agent: "browser-kubernetes",
  hostname: "kubernetes-production-cluster-with-an-intentionally-long-hostname",
  platform: "kubernetes",
  status: "ok",
  last_seen_at: now,
  agent_status: "ok",
  condition: "normal",
  metrics: {},
  meta: { version: "v1.34.8" },
};

const service = (overrides) => ({
  id: 0,
  external_id: "",
  parent_external_id: "",
  name: "",
  kind: "deployment",
  image: "ghcr.io/goauthentik/server:2024.2.0",
  image_digest: "",
  state: "1/1",
  health: "healthy",
  health_detail: "",
  freshness: "current",
  latest_digest: "",
  ports: [],
  labels: {},
  first_seen_at: now,
  last_seen_at: now,
  updated_at: now,
  ...overrides,
});

const services = {
  generated_at: now,
  hosts: [{
    ...host,
    services: [
      service({
        id: 101,
        external_id: "authentik-server-deployment",
        name: "authentik-server-with-a-very-long-deployment-name",
      }),
      service({
        id: 102,
        external_id: "authentik-server-pod-6d48b848d-v4x5d",
        parent_external_id: "authentik-server-deployment",
        name: "authentik-server-pod-with-a-very-long-kubernetes-name-6d48b848d-v4x5d",
        kind: "pod",
        state: "running",
      }),
      service({
        id: 103,
        external_id: "authentik-worker-deployment",
        name: "authentik-worker-with-a-long-deployment-name",
        health: "unhealthy",
        health_detail: "test health detail",
        freshness: "outdated",
      }),
    ],
  }],
};

const agents = {
  generated_at: now,
  agents: [{
    name: host.agent,
    platform: host.platform,
    version: "0.17.0",
    interval_seconds: 30,
    status: "ok",
    created_at: now,
    last_seen_at: now,
  }],
};

const events = {
  generated_at: now,
  events: [
    {
      id: 1,
      service_id: 101,
      kind: "health",
      service: services.hosts[0].services[0].name,
      hostname: host.hostname,
      agent: host.agent,
      from_state: "unknown",
      to_state: "healthy",
      at: now,
    },
    {
      id: 2,
      service_id: 101,
      kind: "state",
      service: services.hosts[0].services[0].name,
      hostname: host.hostname,
      agent: host.agent,
      from_state: "pending",
      to_state: "running",
      at: now,
    },
  ],
};

async function fixtureAPI(page) {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const body = {
      "/api/v1/services": services,
      "/api/v1/agents": agents,
      "/api/v1/events": events,
      "/api/v1/me": { authenticated: false },
    }[path];
    if (body) {
      await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
      return;
    }
    await route.fulfill({ status: 404, contentType: "application/json", body: "{}" });
  });
}

async function loadDashboard(page) {
  await fixtureAPI(page);
  await page.goto("/");
  await expect(page.locator("#hosts tr[data-ext]")).toHaveCount(3);
}

async function expectNoHorizontalPageOverflow(page) {
  const widths = await page.evaluate(() => ({
    document: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
    viewport: window.innerWidth,
  }));
  expect(widths.document).toBeLessThanOrEqual(widths.viewport);
  expect(widths.body).toBeLessThanOrEqual(widths.viewport);
}

test("long Kubernetes labels remain readable and the drawer keeps event timestamps clear", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await loadDashboard(page);

  const details = page.locator('[data-ext="authentik-server-deployment"] [data-service-details]');
  const kind = details.locator(".kind");
  await expect(kind).toHaveText("deployment");
  const [detailsBox, kindBox] = await Promise.all([details.boundingBox(), kind.boundingBox()]);
  expect(detailsBox).not.toBeNull();
  expect(kindBox).not.toBeNull();
  expect(kindBox.x).toBeGreaterThanOrEqual(detailsBox.x);
  expect(kindBox.x + kindBox.width).toBeLessThanOrEqual(detailsBox.x + detailsBox.width + 1);
  expect(kindBox.height).toBeLessThanOrEqual(detailsBox.height + 1);

  await details.click();
  const drawer = page.locator("#drawer");
  await expect(drawer).toBeVisible();
  const event = drawer.locator(".d-events .event-row").first();
  const when = event.locator(".when");
  const what = event.locator(".what");
  const [eventBox, whenBox, whatBox] = await Promise.all([event.boundingBox(), when.boundingBox(), what.boundingBox()]);
  expect(eventBox).not.toBeNull();
  expect(whenBox).not.toBeNull();
  expect(whatBox).not.toBeNull();
  expect(whenBox.x).toBeGreaterThanOrEqual(eventBox.x + 11);
  expect(whenBox.x + whenBox.width).toBeLessThanOrEqual(whatBox.x + 1);

  await page.keyboard.press("Escape");
  await expect(drawer).toBeHidden();
  await expect(details).toBeFocused();
});

test("service filtering updates the real catalogue rows", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await loadDashboard(page);

  const query = page.locator("#q");
  await query.fill("worker");
  await expect(page.locator("#hosts tr[data-ext]")).toHaveCount(1);
  await expect(page.locator('[data-ext="authentik-worker-deployment"]')).toBeVisible();

  await query.fill("");
  await expect(page.locator("#hosts tr[data-ext]")).toHaveCount(3);
});

test("tablet catalogue preserves each Kubernetes kind label", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 1000 });
  await loadDashboard(page);

  const kinds = page.locator("#hosts .kind");
  await expect(kinds).toHaveText(["deployment", "pod", "deployment"]);
  for (const kind of await kinds.all()) await expect(kind).toBeVisible();
  const overflow = await page.locator("#hosts tr[data-ext]").evaluateAll((rows) => rows.map((row) => ({
    clientWidth: row.clientWidth,
    scrollWidth: row.scrollWidth,
  })));
  for (const row of overflow) expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth + 1);
});

test("mobile dashboard and drawer do not create horizontal page overflow", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await loadDashboard(page);

  await expect(page.locator("#hosts tr[data-ext]")).toHaveCount(3);
  await expectNoHorizontalPageOverflow(page);

  await page.locator('[data-ext="authentik-server-deployment"] [data-service-details]').click();
  const drawer = page.locator("#drawer");
  await expect(drawer).toBeVisible();
  const drawerBox = await drawer.boundingBox();
  expect(drawerBox).not.toBeNull();
  expect(drawerBox.x + drawerBox.width).toBeLessThanOrEqual(391);
  const drawerWidth = await drawer.evaluate((element) => ({
    clientWidth: element.clientWidth,
    scrollWidth: element.scrollWidth,
  }));
  expect(drawerWidth.scrollWidth).toBeLessThanOrEqual(drawerWidth.clientWidth + 1);
  await expectNoHorizontalPageOverflow(page);
});
