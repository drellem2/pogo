-- pogo.nvim - Neovim integration for pogo daemon
-- Project navigation, file finding, and code search via pogod HTTP API

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

--- Find file in current project (uses telescope if available)
function M.find_file()
  if not client.ensure_server() then return end

  local has_telescope = pcall(require, "telescope")
  if has_telescope then
    require("pogo.telescope").find_file()
    return
  end

  local root = M.project_root()
  if not root then
    vim.notify("[pogo] not in a project", vim.log.levels.WARN)
    return
  end

  local files = client.get_project_files(root)
  if #files == 0 then
    vim.notify("[pogo] no files found", vim.log.levels.INFO)
    return
  end

  vim.ui.select(files, { prompt = "Find file:" }, function(choice)
    if choice then
      vim.cmd("edit " .. vim.fn.fnameescape(root .. choice))
    end
  end)
end

--- Search current project (uses telescope if available)
function M.search(query)
  if not client.ensure_server() then return end

  local has_telescope = pcall(require, "telescope")
  if has_telescope then
    require("pogo.telescope").search({ query = query })
    return
  end

  local root = M.project_root()
  if not root then
    vim.notify("[pogo] not in a project", vim.log.levels.WARN)
    return
  end

  query = query or vim.fn.input("Zoekt query: ", vim.fn.expand("<cword>"))
  if query == "" then return end

  local resp = client.search(root, query)
  if not resp or not resp.results or not resp.results.files then
    vim.notify("[pogo] no results", vim.log.levels.INFO)
    return
  end

  -- Populate quickfix list
  local items = {}
  for _, file in ipairs(resp.results.files) do
    for _, match in ipairs(file.matches or {}) do
      table.insert(items, {
        filename = root .. file.path,
        lnum = match.line,
        text = vim.trim(match.content),
      })
    end
  end

  if #items == 0 then
    vim.notify("[pogo] no results for: " .. query, vim.log.levels.INFO)
    return
  end

  vim.fn.setqflist(items, "r")
  vim.fn.setqflist({}, "a", { title = "pogo search: " .. query })
  vim.cmd("copen")
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
  local icons = { ready = "✓", indexing = "⟳", stale = "!", unindexed = "?" }
  local lines = {}
  for _, s in ipairs(statuses) do
    local icon = icons[s.indexing_status] or "?"
    table.insert(lines, string.format(" %s  %s (%d files)", icon, s.path, s.file_count))
  end
  vim.notify(table.concat(lines, "\n"), vim.log.levels.INFO)
end

--- Lualine component: returns project name with status indicator
--- Caches status to avoid HTTP calls on every render
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
  local name = M.project_name(root)

  local statuses = client.get_status()
  local icons = { ready = "✓", indexing = "⟳", stale = "!", unindexed = "?" }
  local result = string.format("pogo[%s]", name)
  for _, s in ipairs(statuses) do
    if s.path == root then
      local icon = icons[s.indexing_status] or "?"
      result = string.format("pogo[%s %s]", name, icon)
      break
    end
  end

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
  vim.api.nvim_create_user_command("PogoFindFile", function() M.find_file() end, {
    desc = "Find file in current pogo project",
  })
  vim.api.nvim_create_user_command("PogoSearch", function(cmd_opts)
    M.search(cmd_opts.args ~= "" and cmd_opts.args or nil)
  end, {
    desc = "Search current pogo project",
    nargs = "?",
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
        find_file = require("pogo.telescope").find_file,
        search = require("pogo.telescope").search,
        status = require("pogo.telescope").status,
      },
    })
  end
end

return M
