-- pogo/client.lua - HTTP client for pogod daemon
local M = {}

M.base_url = (function()
  local port = vim.env.POGO_PORT or "10000"
  return "http://localhost:" .. port
end)()
M._server_started = false

--- Make an HTTP request to pogod using curl
---@param method string HTTP method (GET or POST)
---@param path string URL path (e.g. "/health")
---@param body string|nil JSON body for POST requests
---@return table|nil result Parsed JSON response, or nil on error
---@return string|nil error Error message if request failed
local function request(method, path, body)
  local url = M.base_url .. path
  local cmd = { "curl", "-s", "-X", method }
  if body then
    table.insert(cmd, "-H")
    table.insert(cmd, "Content-Type: application/json")
    table.insert(cmd, "-d")
    table.insert(cmd, body)
  end
  table.insert(cmd, url)

  local result = vim.system(cmd, { text = true }):wait()
  if result.code ~= 0 then
    return nil, "curl failed: " .. (result.stderr or "unknown error")
  end
  if not result.stdout or result.stdout == "" then
    return nil, "empty response"
  end

  local ok, decoded = pcall(vim.json.decode, result.stdout)
  if not ok then
    -- Some endpoints return plain text (e.g. /health)
    return result.stdout, nil
  end
  return decoded, nil
end

--- Check if pogod is running
---@return boolean
function M.health_check()
  local _, err = request("GET", "/health")
  return err == nil
end

--- Start pogod server
---@return boolean success
function M.start_server()
  if vim.fn.executable("pogod") ~= 1 then
    vim.notify("[pogo] pogod not found in PATH", vim.log.levels.ERROR)
    return false
  end
  vim.fn.jobstart({ "pogod" }, { detach = true })
  -- Wait for server to become available
  for _ = 1, 4 do
    vim.wait(500, function() end)
    if M.health_check() then
      M._server_started = true
      return true
    end
  end
  vim.notify("[pogo] failed to start pogod", vim.log.levels.ERROR)
  return false
end

--- Ensure pogod is running, starting it if needed
---@return boolean
function M.ensure_server()
  if M.health_check() then
    M._server_started = true
    return true
  end
  return M.start_server()
end

--- Get all known projects
---@return table[] projects Array of {id=number, path=string}
function M.get_projects()
  local resp, err = request("GET", "/projects")
  if err then return {} end
  return resp or {}
end

--- Visit (register) a path and get its project root
---@param path string Absolute path to file or directory
---@return table|nil response {project={id=number, path=string}}
function M.visit(path)
  local body = vim.json.encode({ path = path })
  local resp, err = request("POST", "/file", body)
  if err then return nil end
  return resp
end

--- Get the project root for a given path
---@param path string
---@return string|nil root Project root path or nil
function M.project_root(path)
  local resp = M.visit(path)
  if resp and resp.project then
    return resp.project.path
  end
  return nil
end

--- Get the status of all known projects
---@return table[] statuses Array of {id=number, path=string}
function M.get_status()
  local resp, err = request("GET", "/status")
  if err then return {} end
  return resp or {}
end

return M
