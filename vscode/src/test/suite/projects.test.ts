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

  test("getChildren returns ProjectItems from projects API", async () => {
    sandbox.stub(client, "getProjects").resolves([
      { id: 1, path: "/home/user/project-a" },
      { id: 2, path: "/home/user/project-b" },
    ]);

    const provider = new ProjectsProvider();
    const children = await provider.getChildren();

    assert.strictEqual(children.length, 2);
    assert.ok(children[0] instanceof ProjectItem);
    assert.strictEqual(children[0].label, "project-a");
    assert.strictEqual(children[1].label, "project-b");
  });

  test("getChildren returns empty when no projects", async () => {
    sandbox.stub(client, "getProjects").resolves([]);

    const provider = new ProjectsProvider();
    const children = await provider.getChildren();

    assert.strictEqual(children.length, 0);
  });

  test("ProjectItem shows the project path as description", async () => {
    sandbox.stub(client, "getProjects").resolves([
      { id: 1, path: "/home/user/project-a" },
    ]);

    const provider = new ProjectsProvider();
    const children = await provider.getChildren();
    const item = children[0];

    assert.ok(
      item.description === "/home/user/project-a",
      "Description should be the project path"
    );
  });

  test("ProjectItem has open folder command", async () => {
    sandbox.stub(client, "getProjects").resolves([
      { id: 1, path: "/home/user/project-a" },
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
});
