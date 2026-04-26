const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

function loadDashboardGlobals(windowOverrides = {}) {
  const source = fs.readFileSync(path.join(__dirname, "../dashboard.js"), "utf8");
  const window = {
    GOMODEL_BASE_PATH: "/g",
    ...windowOverrides,
  };
  const context = {
    console,
    window,
  };

  vm.createContext(context);
  vm.runInContext(source, context);
  return context;
}

function loadLayoutBootstrap(basePath = "/g") {
  const layout = fs.readFileSync(
    path.join(__dirname, "../../../templates/layout.html"),
    "utf8",
  );
  const match = layout.match(/<script>\s*([\s\S]*?)\s*<\/script>/i);
  assert.ok(match, "expected inline dashboard bootstrap script");

  const fetchCalls = [];
  const historyCalls = [];
  const window = {
    location: {
      href: "http://localhost/g/admin/dashboard",
      origin: "http://localhost",
    },
    fetch(input, init) {
      fetchCalls.push({ input, init });
      return Promise.resolve({ input, init });
    },
    history: {
      pushState(state, title, url) {
        historyCalls.push({ method: "pushState", state, title, url });
      },
      replaceState(state, title, url) {
        historyCalls.push({ method: "replaceState", state, title, url });
      },
    },
  };
  const context = {
    console,
    Request,
    URL,
    window,
  };
  const script = match[1].replace(
    'const basePath = "{{.BasePath}}";',
    `const basePath = "${basePath}";`,
  );

  vm.createContext(context);
  vm.runInContext(script, context);
  return { fetchCalls, historyCalls, window };
}

test("dashboardPath delegates to the layout path prefix helper", () => {
  const context = loadDashboardGlobals({
    gomodelPath(pathValue) {
      return `/g${pathValue}`;
    },
  });

  assert.equal(context.dashboardPath("/admin/dashboard/usage"), "/g/admin/dashboard/usage");
});

test("dashboardUnprefixedPath strips only the configured base path boundary", () => {
  const context = loadDashboardGlobals();

  assert.equal(context.dashboardUnprefixedPath("/g/admin/dashboard"), "/admin/dashboard");
  assert.equal(context.dashboardUnprefixedPath("/g"), "/");
  assert.equal(context.dashboardUnprefixedPath("/gopher/admin/dashboard"), "/gopher/admin/dashboard");
});

test("layout gomodelPath prefixes root-relative dashboard URLs idempotently", () => {
  const { window } = loadLayoutBootstrap();

  assert.equal(window.gomodelPath("/admin/api/v1/models"), "/g/admin/api/v1/models");
  assert.equal(window.gomodelPath("/g/admin/api/v1/models"), "/g/admin/api/v1/models");
  assert.equal(window.gomodelPath("https://example.com/admin/api/v1/models"), "https://example.com/admin/api/v1/models");
});

test("layout fetch wrapper prefixes string, URL, and Request inputs", async() => {
  const { fetchCalls, window } = loadLayoutBootstrap();

  await window.fetch("/admin/api/v1/models", { headers: { Accept: "application/json" } });
  assert.equal(fetchCalls[0].input, "/g/admin/api/v1/models");
  assert.equal(fetchCalls[0].init.headers.Accept, "application/json");

  await window.fetch(new URL("http://localhost/admin/api/v1/models?limit=1"));
  assert.equal(fetchCalls[1].input.toString(), "http://localhost/g/admin/api/v1/models?limit=1");

  const crossOriginURL = new URL("http://other-origin.example.com/admin/api/v1/models?limit=1");
  await window.fetch(crossOriginURL);
  assert.equal(fetchCalls[2].input.toString(), crossOriginURL.toString());

  await window.fetch(new URL("http://localhost/g/admin/api/v1/models?limit=1"));
  assert.equal(fetchCalls[3].input.toString(), "http://localhost/g/admin/api/v1/models?limit=1");

  const request = new Request("http://localhost/admin/api/v1/models?limit=1", {
    headers: { Authorization: "Bearer test" },
  });
  await window.fetch(request);
  assert.ok(fetchCalls[4].input instanceof Request);
  assert.equal(fetchCalls[4].input.url, "http://localhost/g/admin/api/v1/models?limit=1");
  assert.equal(fetchCalls[4].input.headers.get("authorization"), "Bearer test");
});

test("layout fetch wrapper leaves cross-origin Request inputs unchanged", async() => {
  const { fetchCalls, window } = loadLayoutBootstrap();
  const request = new Request("http://api.example.com/admin/api/v1/models");

  await window.fetch(request);

  assert.strictEqual(fetchCalls[0].input, request);
});

test("layout history wrapper prefixes dashboard navigation URLs", () => {
  const { historyCalls, window } = loadLayoutBootstrap();

  window.history.pushState(null, "", "/admin/dashboard/usage");
  window.history.replaceState(null, "", "/g/admin/dashboard/models");

  assert.deepEqual(historyCalls, [
    {
      method: "pushState",
      state: null,
      title: "",
      url: "/g/admin/dashboard/usage",
    },
    {
      method: "replaceState",
      state: null,
      title: "",
      url: "/g/admin/dashboard/models",
    },
  ]);
});
