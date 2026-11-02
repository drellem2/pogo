import * as vscode from "vscode";
import * as client from "./client";

export class ProjectItem extends vscode.TreeItem {
  constructor(
    public readonly project: client.Project,
    collapsibleState: vscode.TreeItemCollapsibleState
  ) {
    const name = project.path.replace(/\/$/, "").split("/").pop() || project.path;
    super(name, collapsibleState);

    this.description = project.path;
    this.tooltip = project.path;
    this.contextValue = "project";

    this.command = {
      command: "vscode.openFolder",
      title: "Open Project",
      arguments: [vscode.Uri.file(project.path), { forceNewWindow: false }],
    };
  }
}

export class ProjectsProvider implements vscode.TreeDataProvider<ProjectItem> {
  private _onDidChangeTreeData = new vscode.EventEmitter<ProjectItem | undefined>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  refresh(): void {
    this._onDidChangeTreeData.fire(undefined);
  }

  getTreeItem(element: ProjectItem): vscode.TreeItem {
    return element;
  }

  async getChildren(): Promise<ProjectItem[]> {
    const projects = await client.getProjects();
    return projects.map(
      (p) => new ProjectItem(p, vscode.TreeItemCollapsibleState.None)
    );
  }
}
