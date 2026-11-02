import * as http from "http";
import * as vscode from "vscode";

export interface Project {
  id: number;
  path: string;
}

export interface VisitResponse {
  project: Project;
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
