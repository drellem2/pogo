-- pogo.nvim - Neovim integration for pogo daemon
-- Project registration, switching, and status via pogod HTTP API

local M = {}
local client = require("pogo.client")

M.config = {
  auto_register = true,  -- Auto-register projects on BufEnter
  auto_start = true,     -- Auto-start pogod if not running
}

-- Cache: path -> project root (expires after 10 min)
local visit_cache = {}
local cache_ttl = 600 -- seconds

local function cached_project_root(path)
  local entry = visit_cache[path]
  if entry and (os.time() - entry.time) < cache_ttl then
    return entry.root
  end
  local root = client.project_root(path)
  if root then
    visit_cache[path] = { root = root, time = os.time() }
  end
  return root
end

--- Get the project root for the current buffer
---@return string|nil
function M.project_root()
  local dir = vim.fn.expand("%:p:h")
  if dir == "" then
    dir = vim.fn.getcwd()
  end
  return cached_project_root(dir)
end

--- Get the project name from the root path
---@param root string|nil
---@return string
function M.project_name(root)
  root = root or M.project_root()
  if not root then return "-" end
  return vim.fn.fnamemodify(root:gsub("/$", ""), ":t")
end

--- Switch to a known project (uses telescope if available, otherwise vim.ui.select)
function M.switch_project()
  if not client.ensure_server() then return end

  local has_telescope = pcall(require, "telescope")
  if has_telescope then
    require("pogo.telescope").projects()
    return
  end

  local projects = client.get_projects()
  if #projects == 0 then
    vim.notify("[pogo] no projects found", vim.log.levels.INFO)
    return
  end

  local paths = {}
  for _, proj in ipairs(projects) do
    table.insert(paths, proj.path)
  end

  vim.ui.select(paths, { prompt = "Switch to project:" }, function(choice)
    if choice then
      vim.cmd("cd " .. vim.fn.fnameescape(choice))
      vim.notify("[pogo] switched to " .. choice)
    end
  end)
end

--- Show project status (uses telescope if available)
function M.status()
  if not client.ensure_server() then return end

  local has_telescope = pcall(require, "telescope")
  if has_telescope then
    require("pogo.telescope").status()
    return
  end

  local statuses = client.get_status()
  if #statuses == 0 then
    vim.notify("[pogo] no project status available", vim.log.levels.INFO)
    return
  end
  local lines = {}
  for _, s in ipairs(statuses) do
    table.insert(lines, " " .. s.path)
  end
  vim.notify(table.concat(lines, "\n"), vim.log.levels.INFO)
end

--- Lualine component: returns the current project name, e.g. "pogo[myrepo]"
--- Caches the result to avoid HTTP calls on every render
---@return string
local status_cache = { value = "", time = 0 }
local status_cache_ttl = 30 -- seconds

function M.statusline()
  if (os.time() - status_cache.time) < status_cache_ttl then
    return status_cache.value
  end

  local root = M.project_root()
  if not root then
    status_cache = { value = "", time = os.time() }
    return ""
  end
  local result = string.format("pogo[%s]", M.project_name(root))

  status_cache = { value = result, time = os.time() }
  return result
end

--- Auto-register on BufEnter
local function on_buf_enter()
  local path = vim.fn.expand("%:p:h")
  if path == "" or path:match("^%w+://") then return end
  -- Fire and forget: register with pogo in background
  cached_project_root(path)
end

--- Setup the plugin
function M.setup(opts)
  opts = opts or {}
  M.config = vim.tbl_deep_extend("force", M.config, opts)

  -- Auto-start server
  if M.config.auto_start then
    vim.defer_fn(function()
      client.ensure_server()
    end, 0)
  end

  -- Auto-register projects on BufEnter
  if M.config.auto_register then
    vim.api.nvim_create_autocmd("BufEnter", {
      group = vim.api.nvim_create_augroup("pogo_auto_register", { clear = true }),
      callback = on_buf_enter,
    })
  end

  -- Create user commands
  vim.api.nvim_create_user_command("PogoProjects", function() M.switch_project() end, {
    desc = "Switch to a pogo project",
  })
  vim.api.nvim_create_user_command("PogoStatus", function() M.status() end, {
    desc = "Show pogo project status",
  })

  -- Register telescope extension if telescope is available
  local has_telescope = pcall(require, "telescope")
  if has_telescope then
    require("telescope").register_extension({
      exports = {
        projects = require("pogo.telescope").projects,
        status = require("pogo.telescope").status,
      },
    })
  end
end

return M
