import * as assert from "assert";
import * as http from "http";
import * as client from "../../client";

suite("Pogo Client", () => {
  let server: http.Server;
  let port: number;
  let originalEnv: string | undefined;

  // Spin up a local HTTP server to mock the pogo daemon
  suiteSetup((done) => {
    originalEnv = process.env.POGO_PORT;
    server = http.createServer((req, res) => {
      let body = "";
      req.on("data", (chunk) => (body += chunk));
      req.on("end", () => {
        handleRequest(req, res, body);
      });
    });
    server.listen(0, () => {
      const addr = server.address();
      if (addr && typeof addr === "object") {
        port = addr.port;
        process.env.POGO_PORT = String(port);
      }
      done();
    });
  });

  suiteTeardown((done) => {
    if (originalEnv === undefined) {
      delete process.env.POGO_PORT;
    } else {
      process.env.POGO_PORT = originalEnv;
    }
    server.close(done);
  });

  const routes: Record<string, (req: http.IncomingMessage, body: string) => unknown> = {};

  function handleRequest(
    req: http.IncomingMessage,
    res: http.ServerResponse,
    body: string
  ) {
    const url = new URL(req.url || "/", `http://localhost:${port}`);
    const key = `${req.method} ${url.pathname}`;
    const handler = routes[key];

    if (handler) {
      const result = handler(req, body);
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify(result));
    } else {
      res.writeHead(404);
      res.end("Not found");
    }
  }

  function setRoute(
    method: string,
    path: string,
    handler: (req: http.IncomingMessage, body: string) => unknown
  ) {
    routes[`${method} ${path}`] = handler;
  }

  function clearRoutes() {
    for (const key of Object.keys(routes)) {
      delete routes[key];
    }
  }

  setup(() => {
    clearRoutes();
  });

  suite("healthCheck", () => {
    test("returns true when server responds", async () => {
      setRoute("GET", "/health", () => ({ status: "ok" }));
      const result = await client.healthCheck();
      assert.strictEqual(result, true);
    });

    test("returns false when server is down", async () => {
      // No route set — server will 404, but healthCheck catches errors
      // Actually, we need to test with the server not responding at all.
      // Use a different port that nothing listens on.
      const saved = process.env.POGO_PORT;
      process.env.POGO_PORT = "19999";
      try {
        const result = await client.healthCheck();
        assert.strictEqual(result, false);
      } finally {
        process.env.POGO_PORT = saved;
      }
    });
  });

  suite("getProjects", () => {
    test("returns list of projects", async () => {
      setRoute("GET", "/projects", () => [
        { id: 1, path: "/home/user/project-a" },
        { id: 2, path: "/home/user/project-b" },
      ]);

      const projects = await client.getProjects();
      assert.strictEqual(projects.length, 2);
      assert.strictEqual(projects[0].path, "/home/user/project-a");
      assert.strictEqual(projects[1].id, 2);
    });

    test("returns empty array on error", async () => {
      const saved = process.env.POGO_PORT;
      process.env.POGO_PORT = "19999";
      try {
        const projects = await client.getProjects();
        assert.deepStrictEqual(projects, []);
      } finally {
        process.env.POGO_PORT = saved;
      }
    });
  });

  suite("visit", () => {
    test("registers a path and returns project", async () => {
      setRoute("POST", "/file", (_req, body) => {
        const parsed = JSON.parse(body);
        return {
          project: { id: 1, path: parsed.path },
        };
      });

      const result = await client.visit("/home/user/myproject");
      assert.ok(result);
      assert.strictEqual(result!.project.path, "/home/user/myproject");
    });

    test("returns null on error", async () => {
      const saved = process.env.POGO_PORT;
      process.env.POGO_PORT = "19999";
      try {
        const result = await client.visit("/nonexistent");
        assert.strictEqual(result, null);
      } finally {
        process.env.POGO_PORT = saved;
      }
    });
  });
});
