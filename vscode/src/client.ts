import * as http from "http";
import * as vscode from "vscode";

export interface Project {
  id: number;
  path: string;
}

export interface VisitResponse {
  project: Project;
}

export interface ProjectStatus {
  id: number;
  path: string;
  indexing_status: string;
  file_count: number;
}

export interface IndexedProject {
  root: string;
  paths: string[];
  indexing_status: string;
}

export interface SearchMatch {
  line: number;
  content: string;
}

export interface SearchFile {
  path: string;
  matches: SearchMatch[];
}

export interface SearchResponse {
  index: {
    root: string;
    paths: string[];
    indexing_status: string;
  };
  results: {
    files: SearchFile[];
  };
  error: string;
}

function getBaseUrl(): string {
  const defaultUrl = process.env.POGO_PORT
    ? `http://localhost:${process.env.POGO_PORT}`
    : "http://localhost:10000";
  return vscode.workspace
    .getConfiguration("pogo")
    .get<string>("serverUrl", defaultUrl);
}

function request(method: string, path: string, body?: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const url = new URL(path, getBaseUrl());
    const options: http.RequestOptions = {
      hostname: url.hostname,
      port: url.port,
      path: url.pathname + url.search,
      method,
      headers: body
        ? { "Content-Type": "application/json" }
        : undefined,
      timeout: 5000,
    };

    const req = http.request(options, (res) => {
      let data = "";
      res.on("data", (chunk) => (data += chunk));
      res.on("end", () => resolve(data));
    });

    req.on("error", (err) => reject(err));
    req.on("timeout", () => {
      req.destroy();
      reject(new Error("request timed out"));
    });

    if (body) {
      req.write(body);
    }
    req.end();
  });
}

async function jsonRequest<T>(method: string, path: string, body?: string): Promise<T> {
  const data = await request(method, path, body);
  return JSON.parse(data) as T;
}

export async function healthCheck(): Promise<boolean> {
  try {
    await request("GET", "/health");
    return true;
  } catch {
    return false;
  }
}

export async function getProjects(): Promise<Project[]> {
  try {
    return await jsonRequest<Project[]>("GET", "/projects");
  } catch {
    return [];
  }
}

export async function visit(path: string): Promise<VisitResponse | null> {
  try {
    return await jsonRequest<VisitResponse>(
      "POST",
      "/file",
      JSON.stringify({ path })
    );
  } catch {
    return null;
  }
}

export async function getProjectFiles(path: string): Promise<string[]> {
  try {
    const encoded = encodeURIComponent(path);
    const resp = await jsonRequest<IndexedProject>(
      "GET",
      `/projects/file?path=${encoded}`
    );
    return resp.paths || [];
  } catch {
    return [];
  }
}

export async function getStatus(): Promise<ProjectStatus[]> {
  try {
    return await jsonRequest<ProjectStatus[]>("GET", "/status");
  } catch {
    return [];
  }
}

let searchPluginCache: string | null = null;

async function getSearchPlugin(): Promise<string | null> {
  if (searchPluginCache) {
    return searchPluginCache;
  }
  try {
    const plugins = await jsonRequest<string[]>("GET", "/plugins");
    const found = plugins.find((p) => p.includes("pogo-plugin-search"));
    if (found) {
      searchPluginCache = found;
    }
    return found || null;
  } catch {
    return null;
  }
}

export async function search(
  projectRoot: string,
  query: string
): Promise<SearchResponse | null> {
  const pluginPath = await getSearchPlugin();
  if (!pluginPath) {
    return null;
  }

  const searchRequest = JSON.stringify({
    type: "search",
    projectRoot,
    string: "10s",
    data: query,
  });

  const body = JSON.stringify({
    plugin: pluginPath,
    value: encodeURIComponent(searchRequest),
  });

  try {
    const resp = await jsonRequest<{ value: string }>("POST", "/plugin", body);
    if (resp.value) {
      const decoded = decodeURIComponent(resp.value);
      return JSON.parse(decoded) as SearchResponse;
    }
    return null;
  } catch {
    return null;
  }
}
