import * as vscode from "vscode";
import * as client from "./client";

const STATUS_ICONS: Record<string, string> = {
  ready: "$(check)",
  indexing: "$(sync~spin)",
  stale: "$(warning)",
  unindexed: "$(circle-outline)",
};

export class ProjectItem extends vscode.TreeItem {
  constructor(
    public readonly project: client.ProjectStatus,
    collapsibleState: vscode.TreeItemCollapsibleState
  ) {
    const name = project.path.replace(/\/$/, "").split("/").pop() || project.path;
    super(name, collapsibleState);

    const icon = STATUS_ICONS[project.indexing_status] || "$(question)";
    this.description = `${icon} ${project.file_count} files`;
    this.tooltip = `${project.path}\nStatus: ${project.indexing_status}\nFiles: ${project.file_count}`;
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
    const statuses = await client.getStatus();
    return statuses.map(
      (s) => new ProjectItem(s, vscode.TreeItemCollapsibleState.None)
    );
  }
}
