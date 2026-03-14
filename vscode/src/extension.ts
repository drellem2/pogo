import * as vscode from "vscode";
import * as path from "path";
import * as client from "./client";
import { ProjectsProvider } from "./projects";

let statusBarItem: vscode.StatusBarItem;
let statusInterval: ReturnType<typeof setInterval> | undefined;

export function activate(context: vscode.ExtensionContext) {
  const projectsProvider = new ProjectsProvider();
  vscode.window.registerTreeDataProvider("pogoProjects", projectsProvider);

  statusBarItem = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Left,
    50
  );
  statusBarItem.command = "pogo.status";
  context.subscriptions.push(statusBarItem);

  context.subscriptions.push(
    vscode.commands.registerCommand("pogo.switchProject", switchProject),
    vscode.commands.registerCommand("pogo.findFile", findFile),
    vscode.commands.registerCommand("pogo.search", searchProject),
    vscode.commands.registerCommand("pogo.status", () => showStatus(projectsProvider))
  );

  // Auto-register on folder open
  const autoRegister = vscode.workspace
    .getConfiguration("pogo")
    .get<boolean>("autoRegister", true);

  if (autoRegister) {
    registerWorkspaceFolders();
    context.subscriptions.push(
      vscode.workspace.onDidChangeWorkspaceFolders(() => registerWorkspaceFolders())
    );
  }

  // Update status bar periodically
  updateStatusBar();
  statusInterval = setInterval(updateStatusBar, 30_000);
  context.subscriptions.push({ dispose: () => clearInterval(statusInterval) });
}

export function deactivate() {
  if (statusInterval) {
    clearInterval(statusInterval);
  }
}

async function registerWorkspaceFolders() {
  const folders = vscode.workspace.workspaceFolders;
  if (!folders) {
    return;
  }
  for (const folder of folders) {
    await client.visit(folder.uri.fsPath);
  }
}

async function updateStatusBar() {
  const healthy = await client.healthCheck();
  if (!healthy) {
    statusBarItem.text = "$(circle-slash) pogo";
    statusBarItem.tooltip = "pogo daemon not running";
    statusBarItem.show();
    return;
  }

  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) {
    statusBarItem.hide();
    return;
  }

  const statuses = await client.getStatus();
  const currentPath = folders[0].uri.fsPath;
  const match = statuses.find(
    (s) => currentPath.startsWith(s.path) || s.path.startsWith(currentPath)
  );

  if (match) {
    const icons: Record<string, string> = {
      ready: "$(check)",
      indexing: "$(sync~spin)",
      stale: "$(warning)",
      unindexed: "$(question)",
    };
    const icon = icons[match.indexing_status] || "";
    const name = match.path.replace(/\/$/, "").split("/").pop();
    statusBarItem.text = `${icon} pogo[${name}]`;
    statusBarItem.tooltip = `${match.path}\nStatus: ${match.indexing_status}\nFiles: ${match.file_count}`;
  } else {
    statusBarItem.text = "$(search) pogo";
    statusBarItem.tooltip = "pogo daemon running";
  }
  statusBarItem.show();
}

async function ensureServer(): Promise<boolean> {
  const healthy = await client.healthCheck();
  if (!healthy) {
    vscode.window.showWarningMessage(
      "pogo daemon is not running. Start it with: pogo server start"
    );
    return false;
  }
  return true;
}

async function switchProject() {
  if (!(await ensureServer())) {
    return;
  }

  const projects = await client.getProjects();
  if (projects.length === 0) {
    vscode.window.showInformationMessage("No projects found");
    return;
  }

  const items = projects.map((p) => ({
    label: path.basename(p.path.replace(/\/$/, "")),
    description: p.path,
    project: p,
  }));

  const selected = await vscode.window.showQuickPick(items, {
    placeHolder: "Switch to project",
  });

  if (selected) {
    const uri = vscode.Uri.file(selected.project.path);
    await vscode.commands.executeCommand("vscode.openFolder", uri);
  }
}

async function getProjectRoot(): Promise<string | null> {
  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) {
    vscode.window.showWarningMessage("No workspace folder open");
    return null;
  }

  const resp = await client.visit(folders[0].uri.fsPath);
  if (!resp || !resp.project) {
    vscode.window.showWarningMessage("Not in a pogo project");
    return null;
  }
  return resp.project.path;
}

async function findFile() {
  if (!(await ensureServer())) {
    return;
  }

  const root = await getProjectRoot();
  if (!root) {
    return;
  }

  const files = await client.getProjectFiles(root);
  if (files.length === 0) {
    vscode.window.showInformationMessage("No files found in project");
    return;
  }

  const items = files.map((f) => ({
    label: path.basename(f),
    description: f,
    filePath: f,
  }));

  const selected = await vscode.window.showQuickPick(items, {
    placeHolder: "Find file in project",
    matchOnDescription: true,
  });

  if (selected) {
    const fullPath = path.join(root, selected.filePath);
    const doc = await vscode.workspace.openTextDocument(fullPath);
    await vscode.window.showTextDocument(doc);
  }
}

async function searchProject() {
  if (!(await ensureServer())) {
    return;
  }

  const root = await getProjectRoot();
  if (!root) {
    return;
  }

  const query = await vscode.window.showInputBox({
    placeHolder: "Search query (zoekt syntax)",
    prompt: "Search across project files",
  });

  if (!query) {
    return;
  }

  const resp = await client.search(root, query);
  if (!resp || !resp.results || !resp.results.files || resp.results.files.length === 0) {
    vscode.window.showInformationMessage(`No results for: ${query}`);
    return;
  }

  // Show results in a quick pick with file:line format
  type ResultItem = vscode.QuickPickItem & { filePath: string; line: number };
  const items: ResultItem[] = [];
  for (const file of resp.results.files) {
    for (const match of file.matches) {
      items.push({
        label: `${path.basename(file.path)}:${match.line}`,
        description: file.path,
        detail: match.content.trim(),
        filePath: file.path,
        line: match.line,
      });
    }
  }

  const selected = await vscode.window.showQuickPick(items, {
    placeHolder: `${items.length} results for: ${query}`,
    matchOnDescription: true,
    matchOnDetail: true,
  });

  if (selected) {
    const fullPath = path.join(root, selected.filePath);
    const doc = await vscode.workspace.openTextDocument(fullPath);
    const editor = await vscode.window.showTextDocument(doc);
    const line = Math.max(0, selected.line - 1);
    const range = new vscode.Range(line, 0, line, 0);
    editor.selection = new vscode.Selection(range.start, range.start);
    editor.revealRange(range, vscode.TextEditorRevealType.InCenter);
  }
}

async function showStatus(projectsProvider: ProjectsProvider) {
  projectsProvider.refresh();

  if (!(await ensureServer())) {
    return;
  }

  const statuses = await client.getStatus();
  if (statuses.length === 0) {
    vscode.window.showInformationMessage("No projects registered with pogo");
    return;
  }

  const icons: Record<string, string> = {
    ready: "\u2713",
    indexing: "\u21BB",
    stale: "!",
    unindexed: "?",
  };

  const lines = statuses.map((s) => {
    const icon = icons[s.indexing_status] || "?";
    return `${icon} ${s.path} (${s.file_count} files)`;
  });

  vscode.window.showInformationMessage(lines.join("\n"), { modal: true });
}
