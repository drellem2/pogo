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

  const projects = await client.getProjects();
  const currentPath = folders[0].uri.fsPath;
  const match = projects.find(
    (p) => currentPath.startsWith(p.path) || p.path.startsWith(currentPath)
  );

  if (match) {
    const name = path.basename(match.path.replace(/\/$/, ""));
    statusBarItem.text = `$(repo) pogo[${name}]`;
    statusBarItem.tooltip = match.path;
  } else {
    statusBarItem.text = "$(repo) pogo";
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

async function showStatus(projectsProvider: ProjectsProvider) {
  projectsProvider.refresh();

  if (!(await ensureServer())) {
    return;
  }

  const projects = await client.getProjects();
  if (projects.length === 0) {
    vscode.window.showInformationMessage("No projects registered with pogo");
    return;
  }

  const lines = projects.map((p) => p.path);
  vscode.window.showInformationMessage(
    `${projects.length} projects registered with pogo:\n${lines.join("\n")}`,
    { modal: true }
  );
}
