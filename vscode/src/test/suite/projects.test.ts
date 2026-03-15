import * as assert from "assert";
import * as sinon from "sinon";
import * as vscode from "vscode";
import * as client from "../../client";
import { ProjectsProvider, ProjectItem } from "../../projects";

suite("Projects Tree Provider", () => {
  let sandbox: sinon.SinonSandbox;

  setup(() => {
    sandbox = sinon.createSandbox();
  });

  teardown(() => {
    sandbox.restore();
  });

  test("getChildren returns ProjectItems from status API", async () => {
    sandbox.stub(client, "getStatus").resolves([
      { id: 1, path: "/home/user/project-a", indexing_status: "ready", file_count: 42 },
      { id: 2, path: "/home/user/project-b", indexing_status: "indexing", file_count: 100 },
    ]);

    const provider = new ProjectsProvider();
    const children = await provider.getChildren();

    assert.strictEqual(children.length, 2);
    assert.ok(children[0] instanceof ProjectItem);
    assert.strictEqual(children[0].label, "project-a");
    assert.strictEqual(children[1].label, "project-b");
  });

  test("getChildren returns empty when no projects", async () => {
    sandbox.stub(client, "getStatus").resolves([]);

    const provider = new ProjectsProvider();
    const children = await provider.getChildren();

    assert.strictEqual(children.length, 0);
  });

  test("ProjectItem displays correct status description", async () => {
    sandbox.stub(client, "getStatus").resolves([
      { id: 1, path: "/home/user/project-a", indexing_status: "ready", file_count: 42 },
    ]);

    const provider = new ProjectsProvider();
    const children = await provider.getChildren();
    const item = children[0];

    assert.ok(
      (item.description as string).includes("42 files"),
      "Description should include file count"
    );
  });

  test("ProjectItem has open folder command", async () => {
    sandbox.stub(client, "getStatus").resolves([
      { id: 1, path: "/home/user/project-a", indexing_status: "ready", file_count: 42 },
    ]);

    const provider = new ProjectsProvider();
    const children = await provider.getChildren();
    const item = children[0];

    assert.ok(item.command, "ProjectItem should have a command");
    assert.strictEqual(item.command!.command, "vscode.openFolder");
  });

  test("refresh fires onDidChangeTreeData event", () => {
    const provider = new ProjectsProvider();
    let fired = false;
    provider.onDidChangeTreeData(() => {
      fired = true;
    });

    provider.refresh();
    assert.ok(fired, "refresh should fire onDidChangeTreeData");
  });

  test("ProjectItem shows correct status icons", async () => {
    const statuses = [
      { id: 1, path: "/a/ready-proj", indexing_status: "ready", file_count: 10 },
      { id: 2, path: "/a/indexing-proj", indexing_status: "indexing", file_count: 20 },
      { id: 3, path: "/a/stale-proj", indexing_status: "stale", file_count: 30 },
      { id: 4, path: "/a/unindexed-proj", indexing_status: "unindexed", file_count: 0 },
    ];

    sandbox.stub(client, "getStatus").resolves(statuses);

    const provider = new ProjectsProvider();
    const children = await provider.getChildren();

    assert.ok(
      (children[0].description as string).includes("$(check)"),
      "Ready status should show check icon"
    );
    assert.ok(
      (children[1].description as string).includes("$(sync~spin)"),
      "Indexing status should show sync icon"
    );
    assert.ok(
      (children[2].description as string).includes("$(warning)"),
      "Stale status should show warning icon"
    );
    assert.ok(
      (children[3].description as string).includes("$(circle-outline)"),
      "Unindexed status should show circle outline icon"
    );
  });
});
