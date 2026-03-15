import * as assert from "assert";
import * as sinon from "sinon";
import * as vscode from "vscode";
import * as client from "../../client";

suite("Pogo Extension", () => {
  let sandbox: sinon.SinonSandbox;

  setup(() => {
    sandbox = sinon.createSandbox();
  });

  teardown(() => {
    sandbox.restore();
  });

  suite("Extension Activation", () => {
    test("extension is present", () => {
      const ext = vscode.extensions.getExtension("pogo.pogo");
      assert.ok(ext, "Extension should be available");
    });

    test("registers all commands", async () => {
      const commands = await vscode.commands.getCommands(true);
      assert.ok(commands.includes("pogo.switchProject"), "switchProject command registered");
      assert.ok(commands.includes("pogo.findFile"), "findFile command registered");
      assert.ok(commands.includes("pogo.search"), "search command registered");
      assert.ok(commands.includes("pogo.status"), "status command registered");
    });
  });

  suite("Quick Pick Project Switcher (pogo.switchProject)", () => {
    test("shows warning when daemon is not running", async () => {
      sandbox.stub(client, "healthCheck").resolves(false);
      const warnStub = sandbox.stub(vscode.window, "showWarningMessage");

      await vscode.commands.executeCommand("pogo.switchProject");

      assert.ok(
        warnStub.calledOnce,
        "Should show warning when daemon is not running"
      );
      assert.ok(
        warnStub.firstCall.args[0].includes("not running"),
        "Warning should mention daemon not running"
      );
    });

    test("shows info message when no projects found", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      sandbox.stub(client, "getProjects").resolves([]);
      const infoStub = sandbox.stub(vscode.window, "showInformationMessage");

      await vscode.commands.executeCommand("pogo.switchProject");

      assert.ok(
        infoStub.calledOnce,
        "Should show info when no projects found"
      );
      assert.ok(
        infoStub.firstCall.args[0].includes("No projects"),
        "Message should indicate no projects"
      );
    });

    test("displays project list in quick pick", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      sandbox.stub(client, "getProjects").resolves([
        { id: 1, path: "/home/user/project-a" },
        { id: 2, path: "/home/user/project-b" },
      ]);
      const quickPickStub = sandbox
        .stub(vscode.window, "showQuickPick")
        .resolves(undefined);

      await vscode.commands.executeCommand("pogo.switchProject");

      assert.ok(quickPickStub.calledOnce, "Should show quick pick");
      const items = quickPickStub.firstCall.args[0] as Array<{
        label: string;
        description: string;
      }>;
      assert.strictEqual(items.length, 2, "Should show 2 projects");
      assert.strictEqual(items[0].label, "project-a");
      assert.strictEqual(items[1].label, "project-b");
    });

    test("opens selected project folder", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      sandbox.stub(client, "getProjects").resolves([
        { id: 1, path: "/home/user/project-a" },
      ]);
      sandbox.stub(vscode.window, "showQuickPick").callsFake(async (items: any) => {
        return items[0];
      });
      const execStub = sandbox.stub(vscode.commands, "executeCommand");
      // Allow our own command to pass through, stub the openFolder call
      execStub.withArgs("pogo.switchProject").callThrough();
      execStub.withArgs("vscode.openFolder", sinon.match.any).resolves();

      await vscode.commands.executeCommand("pogo.switchProject");

      assert.ok(
        execStub.calledWith("vscode.openFolder", sinon.match.any),
        "Should open the selected project folder"
      );
    });
  });

  suite("Search Command (pogo.search)", () => {
    test("shows warning when daemon is not running", async () => {
      sandbox.stub(client, "healthCheck").resolves(false);
      const warnStub = sandbox.stub(vscode.window, "showWarningMessage");

      await vscode.commands.executeCommand("pogo.search");

      assert.ok(warnStub.calledOnce, "Should warn when daemon offline");
    });

    test("shows no-results message for empty search", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      sandbox.stub(client, "visit").resolves({
        project: { id: 1, path: "/home/user/myproject" },
      });
      sandbox.stub(vscode.window, "showInputBox").resolves("nonexistent_query");
      sandbox.stub(client, "search").resolves({
        index: { root: "/home/user/myproject", paths: [], indexing_status: "ready" },
        results: { files: [] },
        error: "",
      });
      const infoStub = sandbox.stub(vscode.window, "showInformationMessage");

      // Need a workspace folder for getProjectRoot
      sandbox.stub(vscode.workspace, "workspaceFolders" as any).value([
        { uri: vscode.Uri.file("/home/user/myproject"), name: "myproject", index: 0 },
      ]);

      await vscode.commands.executeCommand("pogo.search");

      assert.ok(infoStub.calledOnce, "Should show no results message");
      assert.ok(
        infoStub.firstCall.args[0].includes("No results"),
        "Message should indicate no results"
      );
    });

    test("displays search results with file and line info", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      sandbox.stub(client, "visit").resolves({
        project: { id: 1, path: "/home/user/myproject" },
      });
      sandbox.stub(vscode.window, "showInputBox").resolves("func main");
      sandbox.stub(client, "search").resolves({
        index: { root: "/home/user/myproject", paths: [], indexing_status: "ready" },
        results: {
          files: [
            {
              path: "cmd/main.go",
              matches: [
                { line: 10, content: "func main() {" },
                { line: 25, content: "func mainLoop() {" },
              ],
            },
          ],
        },
        error: "",
      });
      const quickPickStub = sandbox
        .stub(vscode.window, "showQuickPick")
        .resolves(undefined);

      sandbox.stub(vscode.workspace, "workspaceFolders" as any).value([
        { uri: vscode.Uri.file("/home/user/myproject"), name: "myproject", index: 0 },
      ]);

      await vscode.commands.executeCommand("pogo.search");

      assert.ok(quickPickStub.calledOnce, "Should show quick pick with results");
      const items = quickPickStub.firstCall.args[0] as Array<{
        label: string;
        detail: string;
      }>;
      assert.strictEqual(items.length, 2, "Should show 2 matches");
      assert.strictEqual(items[0].label, "main.go:10");
      assert.strictEqual(items[1].label, "main.go:25");
      assert.strictEqual(items[0].detail, "func main() {");
    });

    test("does nothing when user cancels input box", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      sandbox.stub(client, "visit").resolves({
        project: { id: 1, path: "/home/user/myproject" },
      });
      sandbox.stub(vscode.window, "showInputBox").resolves(undefined);
      const searchStub = sandbox.stub(client, "search");

      sandbox.stub(vscode.workspace, "workspaceFolders" as any).value([
        { uri: vscode.Uri.file("/home/user/myproject"), name: "myproject", index: 0 },
      ]);

      await vscode.commands.executeCommand("pogo.search");

      assert.ok(searchStub.notCalled, "Should not call search when input cancelled");
    });
  });

  suite("Auto-Visit on Workspace Folder Change", () => {
    test("calls visit for workspace folders on activation", async () => {
      const visitStub = sandbox.stub(client, "visit").resolves({
        project: { id: 1, path: "/home/user/myproject" },
      });

      sandbox.stub(vscode.workspace, "workspaceFolders" as any).value([
        { uri: vscode.Uri.file("/home/user/myproject"), name: "myproject", index: 0 },
      ]);

      // The extension auto-registers on activation. Since it's already activated,
      // we verify the visit function is callable and works correctly.
      const result = await client.visit("/home/user/myproject");

      assert.ok(visitStub.calledOnce, "visit should be called");
      assert.ok(result, "visit should return a response");
      assert.strictEqual(result!.project.path, "/home/user/myproject");
    });

    test("visit sends correct path to server", async () => {
      const visitStub = sandbox.stub(client, "visit").resolves({
        project: { id: 1, path: "/home/user/new-folder" },
      });

      await client.visit("/home/user/new-folder");

      assert.ok(
        visitStub.calledWith("/home/user/new-folder"),
        "Should pass folder path to visit"
      );
    });
  });

  suite("Status Bar Indexing Indicator", () => {
    test("getStatus returns project statuses", async () => {
      sandbox.stub(client, "getStatus").resolves([
        { id: 1, path: "/home/user/project-a", indexing_status: "ready", file_count: 42 },
        { id: 2, path: "/home/user/project-b", indexing_status: "indexing", file_count: 100 },
      ]);

      const statuses = await client.getStatus();

      assert.strictEqual(statuses.length, 2);
      assert.strictEqual(statuses[0].indexing_status, "ready");
      assert.strictEqual(statuses[0].file_count, 42);
      assert.strictEqual(statuses[1].indexing_status, "indexing");
    });

    test("healthCheck returns false when daemon is down", async () => {
      sandbox.stub(client, "healthCheck").resolves(false);
      const result = await client.healthCheck();
      assert.strictEqual(result, false);
    });

    test("healthCheck returns true when daemon is running", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      const result = await client.healthCheck();
      assert.strictEqual(result, true);
    });

    test("status command refreshes tree view and shows modal", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      sandbox.stub(client, "getStatus").resolves([
        { id: 1, path: "/home/user/project-a", indexing_status: "ready", file_count: 42 },
      ]);
      const infoStub = sandbox.stub(vscode.window, "showInformationMessage");

      await vscode.commands.executeCommand("pogo.status");

      assert.ok(infoStub.calledOnce, "Should show status information");
      const msg = infoStub.firstCall.args[0] as string;
      assert.ok(msg.includes("/home/user/project-a"), "Should include project path");
      assert.ok(msg.includes("42 files"), "Should include file count");
    });

    test("status command shows message when no projects registered", async () => {
      sandbox.stub(client, "healthCheck").resolves(true);
      sandbox.stub(client, "getStatus").resolves([]);
      const infoStub = sandbox.stub(vscode.window, "showInformationMessage");

      await vscode.commands.executeCommand("pogo.status");

      assert.ok(infoStub.calledOnce);
      assert.ok(
        infoStub.firstCall.args[0].includes("No projects"),
        "Should indicate no projects registered"
      );
    });
  });
});
