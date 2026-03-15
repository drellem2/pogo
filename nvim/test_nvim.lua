-- test_nvim.lua — Tests for pogo.nvim neovim integration
-- Run with: nvim --headless --noplugin -u NONE -l nvim/test_nvim.lua
--
-- Tests:
--   1. Client module: url_encode/decode, project_root, get_projects, get_status, search
--   2. Init module: project picker lists projects, search populates quickfix,
--      auto-visit on BufEnter, statusline shows indexing status
--   3. Telescope module: entry_maker functions produce correct entries

local pass_count = 0
local fail_count = 0
local errors = {}

local function pass(msg)
  pass_count = pass_count + 1
  print("  PASS: " .. msg)
end

local function fail(msg)
  fail_count = fail_count + 1
  table.insert(errors, msg)
  print("  FAIL: " .. msg)
end

local function assert_eq(expected, actual, msg)
  if expected == actual then
    pass(msg)
  else
    fail(msg .. " (expected '" .. tostring(expected) .. "', got '" .. tostring(actual) .. "')")
  end
end

local function assert_truthy(val, msg)
  if val then
    pass(msg)
  else
    fail(msg .. " (expected truthy, got " .. tostring(val) .. ")")
  end
end

local function assert_nil(val, msg)
  if val == nil then
    pass(msg)
  else
    fail(msg .. " (expected nil, got " .. tostring(val) .. ")")
  end
end

local function assert_table_len(expected, tbl, msg)
  if type(tbl) == "table" and #tbl == expected then
    pass(msg)
  else
    local actual = type(tbl) == "table" and #tbl or "not a table"
    fail(msg .. " (expected length " .. expected .. ", got " .. tostring(actual) .. ")")
  end
end

-------------------------------------------------------------------------------
-- Setup: configure package.path so require("pogo.*") finds our modules
-------------------------------------------------------------------------------

-- Get the directory this test file lives in
local script_dir = debug.getinfo(1, "S").source:match("@?(.*/)")
if not script_dir then
  script_dir = "./"
end
-- Add nvim/lua/ to package.path so require("pogo.client") works
package.path = script_dir .. "lua/?.lua;" .. script_dir .. "lua/?/init.lua;" .. package.path

-------------------------------------------------------------------------------
-- Mock layer: stub vim globals for modules that depend on them
-------------------------------------------------------------------------------

-- Capture state for assertions
local mock_state = {
  notifications = {},
  system_calls = {},
  autocmds = {},
  augroups = {},
  user_commands = {},
  qflist = {},
  qflist_props = {},
  deferred = {},
  jobstarts = {},
  ui_select_calls = {},
  cmds = {},
}

-- Mock vim.system responses (keyed by URL path)
local mock_responses = {}

local function set_mock_response(url_suffix, response)
  mock_responses[url_suffix] = response
end

local function clear_mocks()
  mock_responses = {}
  mock_state = {
    notifications = {},
    system_calls = {},
    autocmds = {},
    augroups = {},
    user_commands = {},
    qflist = {},
    qflist_props = {},
    deferred = {},
    jobstarts = {},
    ui_select_calls = {},
    cmds = {},
  }
end

-- Build the vim mock (only if vim global doesn't exist, i.e., not running in nvim)
if not vim then
  ---@diagnostic disable: lowercase-global
  vim = {}
end

-- Always override these to control test behavior
vim.log = vim.log or { levels = { DEBUG = 0, INFO = 1, WARN = 2, ERROR = 3 } }

vim.notify = function(msg, level)
  table.insert(mock_state.notifications, { msg = msg, level = level })
end

vim.json = vim.json or {}
vim.json.encode = vim.json.encode or function(val)
  -- Minimal JSON encoder for test data
  if type(val) == "string" then
    return '"' .. val:gsub('"', '\\"'):gsub("\n", "\\n") .. '"'
  elseif type(val) == "number" then
    return tostring(val)
  elseif type(val) == "boolean" then
    return tostring(val)
  elseif type(val) == "table" then
    -- Check if array
    if #val > 0 or next(val) == nil then
      local parts = {}
      for _, v in ipairs(val) do
        table.insert(parts, vim.json.encode(v))
      end
      return "[" .. table.concat(parts, ",") .. "]"
    else
      local parts = {}
      for k, v in pairs(val) do
        table.insert(parts, '"' .. k .. '":' .. vim.json.encode(v))
      end
      return "{" .. table.concat(parts, ",") .. "}"
    end
  end
  return "null"
end

vim.json.decode = vim.json.decode or function(str)
  -- Minimal JSON decoder for tests (handles the subset produced by our encoder)
  if str == "[]" then return {} end
  if str == "{}" then return {} end
  -- Use Lua load to parse JSON-like strings (safe for test data)
  -- Convert JSON syntax to Lua table syntax
  local lua_str = str
  -- Replace JSON null with nil-compatible value
  lua_str = lua_str:gsub(":null", ":nil")
  -- Replace JSON true/false (already valid Lua)
  -- Convert JSON arrays/objects: JSON uses [] for arrays, Lua uses {}
  lua_str = lua_str:gsub("%[", "{")
  lua_str = lua_str:gsub("%]", "}")
  -- Wrap keys in brackets: "key": -> ["key"]=
  lua_str = lua_str:gsub('"([^"]+)"%s*:', '["%1"]=')
  local fn, err = load("return " .. lua_str)
  if not fn then
    error("vim.json.decode mock: cannot parse: " .. str .. " (" .. (err or "unknown") .. ")")
  end
  return fn()
end

vim.system = function(cmd, opts)
  table.insert(mock_state.system_calls, cmd)
  -- Find URL in the curl command
  local url = cmd[#cmd]
  local response = nil
  local best_len = 0
  for suffix, resp in pairs(mock_responses) do
    if url:find(suffix, 1, true) and #suffix > best_len then
      response = resp
      best_len = #suffix
    end
  end
  return {
    wait = function()
      if not response then
        return { code = 1, stderr = "mock: no response configured", stdout = "" }
      end
      if response.error then
        return { code = 1, stderr = response.error, stdout = "" }
      end
      local stdout = response.stdout
      if type(stdout) == "table" then
        stdout = vim.json.encode(stdout)
      end
      return { code = 0, stdout = stdout or "", stderr = "" }
    end
  }
end

vim.fn = vim.fn or {}
vim.fn.expand = vim.fn.expand or function(expr)
  if expr == "%:p:h" then return "/test/project/src" end
  if expr == "<cword>" then return "testword" end
  return ""
end
vim.fn.getcwd = vim.fn.getcwd or function() return "/test/project" end
vim.fn.executable = vim.fn.executable or function() return 1 end
vim.fn.fnameescape = vim.fn.fnameescape or function(s) return s end
vim.fn.fnamemodify = vim.fn.fnamemodify or function(path, mod)
  if mod == ":t" then
    return path:match("([^/]+)$") or path
  end
  return path
end
vim.fn.input = vim.fn.input or function(_, default) return default or "" end
vim.fn.jobstart = vim.fn.jobstart or function(cmd, opts)
  table.insert(mock_state.jobstarts, { cmd = cmd, opts = opts })
  return 1
end
vim.fn.setqflist = vim.fn.setqflist or function(items, action, props)
  if action == "r" then
    mock_state.qflist = items
  elseif action == "a" then
    mock_state.qflist_props = props or {}
  end
end

vim.cmd = vim.cmd or function(cmd_str)
  table.insert(mock_state.cmds, cmd_str)
end

vim.api = vim.api or {}
vim.api.nvim_create_autocmd = vim.api.nvim_create_autocmd or function(event, opts)
  table.insert(mock_state.autocmds, { event = event, opts = opts })
end
vim.api.nvim_create_augroup = vim.api.nvim_create_augroup or function(name, opts)
  table.insert(mock_state.augroups, { name = name, opts = opts })
  return 1
end
vim.api.nvim_create_user_command = vim.api.nvim_create_user_command or function(name, fn, opts)
  mock_state.user_commands[name] = { fn = fn, opts = opts }
end

vim.tbl_deep_extend = vim.tbl_deep_extend or function(behavior, ...)
  local result = {}
  for _, tbl in ipairs({...}) do
    for k, v in pairs(tbl) do
      result[k] = v
    end
  end
  return result
end

vim.trim = vim.trim or function(s)
  return s:match("^%s*(.-)%s*$")
end

vim.defer_fn = vim.defer_fn or function(fn, ms)
  table.insert(mock_state.deferred, { fn = fn, ms = ms })
end

vim.wait = vim.wait or function(ms, fn)
  -- no-op in tests
end

vim.ui = vim.ui or {}
vim.ui.select = vim.ui.select or function(items, opts, on_choice)
  table.insert(mock_state.ui_select_calls, { items = items, opts = opts })
  -- Auto-select first item for testing
  if #items > 0 then
    on_choice(items[1])
  end
end

-------------------------------------------------------------------------------
-- Test Suite 1: Client module
-------------------------------------------------------------------------------

print("=== pogo.nvim tests ===")
print("")
print("--- Client module ---")

-- Force fresh module load
package.loaded["pogo.client"] = nil
local client = require("pogo.client")

-- Test: health_check succeeds when server responds
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
local healthy = client.health_check()
assert_truthy(healthy, "health_check returns true when server responds")

-- Test: health_check fails when server is down
clear_mocks()
local unhealthy = client.health_check()
assert_eq(false, unhealthy, "health_check returns false when server is down")

-- Test: get_projects returns project list
clear_mocks()
set_mock_response("/projects", {
  stdout = { { id = 1, path = "/home/user/project-a" }, { id = 2, path = "/home/user/project-b" } }
})
local projects = client.get_projects()
assert_table_len(2, projects, "get_projects returns 2 projects")
assert_eq("/home/user/project-a", projects[1].path, "get_projects first project path")
assert_eq("/home/user/project-b", projects[2].path, "get_projects second project path")

-- Test: get_projects returns empty on error
clear_mocks()
local empty_projects = client.get_projects()
assert_table_len(0, empty_projects, "get_projects returns empty on error")

-- Test: visit returns project info
clear_mocks()
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/home/user/project-a" } }
})
local visit_resp = client.visit("/home/user/project-a/src/main.go")
assert_truthy(visit_resp, "visit returns response")
assert_eq("/home/user/project-a", visit_resp.project.path, "visit returns project path")

-- Test: project_root extracts path from visit
clear_mocks()
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/home/user/myrepo" } }
})
local root = client.project_root("/home/user/myrepo/src")
assert_eq("/home/user/myrepo", root, "project_root returns correct root")

-- Test: project_root returns nil on error
clear_mocks()
local no_root = client.project_root("/nowhere")
assert_nil(no_root, "project_root returns nil when visit fails")

-- Test: get_status returns status list
clear_mocks()
set_mock_response("/status", {
  stdout = {
    { id = 1, path = "/home/user/proj", indexing_status = "ready", file_count = 42 },
    { id = 2, path = "/home/user/proj2", indexing_status = "indexing", file_count = 10 },
  }
})
local statuses = client.get_status()
assert_table_len(2, statuses, "get_status returns 2 statuses")
assert_eq("ready", statuses[1].indexing_status, "get_status first project is ready")
assert_eq("indexing", statuses[2].indexing_status, "get_status second project is indexing")

-- Test: get_status returns empty on error
clear_mocks()
local empty_status = client.get_status()
assert_table_len(0, empty_status, "get_status returns empty on error")

-- Test: get_project_files returns file list
clear_mocks()
set_mock_response("/projects/file", {
  stdout = { paths = { "/src/main.go", "/src/util.go", "/README.md" } }
})
local files = client.get_project_files("/home/user/proj")
assert_table_len(3, files, "get_project_files returns 3 files")
assert_eq("/src/main.go", files[1], "get_project_files first file")

-- Test: get_project_files returns empty on error
clear_mocks()
local empty_files = client.get_project_files("/nowhere")
assert_table_len(0, empty_files, "get_project_files returns empty on error")

-- Test: ensure_server returns true when healthy
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
local ensured = client.ensure_server()
assert_truthy(ensured, "ensure_server returns true when server is healthy")

-- Test: search returns decoded results
clear_mocks()
client._search_plugin = nil
set_mock_response("/plugins", {
  stdout = { "pogo-plugin-search" }
})
-- URL-encode a search response for the mock
local search_result = {
  results = {
    files = {
      { path = "/src/main.go", matches = { { line = 10, content = "func main()" } } }
    }
  }
}
-- The search endpoint returns {value: url_encoded_json}
local encoded_value = vim.json.encode(search_result)
-- Simple URL encoding for test
encoded_value = encoded_value:gsub("([^%w%-%.%_%~])", function(c)
  return string.format("%%%02X", string.byte(c))
end)
set_mock_response("/plugin", {
  stdout = { value = encoded_value }
})
local search_resp = client.search("/home/user/proj", "main")
assert_truthy(search_resp, "search returns response")
assert_truthy(search_resp.results, "search response has results")
assert_truthy(search_resp.results.files, "search results have files")
assert_table_len(1, search_resp.results.files, "search returns 1 file")
assert_eq("/src/main.go", search_resp.results.files[1].path, "search result file path")

-- Test: search returns nil when plugin not found
clear_mocks()
client._search_plugin = nil
set_mock_response("/plugins", { stdout = { "some-other-plugin" } })
local no_search = client.search("/proj", "query")
assert_nil(no_search, "search returns nil when search plugin not found")

print("")

-------------------------------------------------------------------------------
-- Test Suite 2: Init module (project picker, search, auto-visit, statusline)
-------------------------------------------------------------------------------

print("--- Init module ---")

-- Reload with fresh state
package.loaded["pogo.client"] = nil
package.loaded["pogo.init"] = nil
package.loaded["pogo"] = nil

-- Make telescope unavailable for these tests (test non-telescope paths)
package.loaded["telescope"] = nil
package.preload["telescope"] = function() error("telescope not available") end

local pogo_client = require("pogo.client")
local pogo = require("pogo")

-- Test: setup creates user commands
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
pogo.setup({})
assert_truthy(mock_state.user_commands["PogoProjects"], "setup creates PogoProjects command")
assert_truthy(mock_state.user_commands["PogoFindFile"], "setup creates PogoFindFile command")
assert_truthy(mock_state.user_commands["PogoSearch"], "setup creates PogoSearch command")
assert_truthy(mock_state.user_commands["PogoStatus"], "setup creates PogoStatus command")

-- Test: setup creates BufEnter autocmd when auto_register is true
local found_bufenter = false
for _, ac in ipairs(mock_state.autocmds) do
  if ac.event == "BufEnter" then
    found_bufenter = true
    break
  end
end
assert_truthy(found_bufenter, "setup creates BufEnter autocmd for auto-register")

-- Test: setup creates augroup for auto_register
local found_augroup = false
for _, ag in ipairs(mock_state.augroups) do
  if ag.name == "pogo_auto_register" then
    found_augroup = true
    break
  end
end
assert_truthy(found_augroup, "setup creates pogo_auto_register augroup")

-- Test: setup defers server start when auto_start is true
assert_truthy(#mock_state.deferred > 0, "setup defers server start")

-- Test: setup with auto_register=false skips autocmd
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
pogo.setup({ auto_register = false })
found_bufenter = false
for _, ac in ipairs(mock_state.autocmds) do
  if ac.event == "BufEnter" then
    found_bufenter = true
    break
  end
end
assert_eq(false, found_bufenter, "setup skips BufEnter autocmd when auto_register=false")

-- Test: switch_project calls vim.ui.select with project paths (non-telescope path)
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
set_mock_response("/projects", {
  stdout = {
    { id = 1, path = "/home/user/project-a" },
    { id = 2, path = "/home/user/project-b" },
  }
})
pogo.switch_project()
assert_table_len(1, mock_state.ui_select_calls, "switch_project calls vim.ui.select")
assert_table_len(2, mock_state.ui_select_calls[1].items, "switch_project offers 2 projects")
assert_eq("/home/user/project-a", mock_state.ui_select_calls[1].items[1],
  "switch_project first item is project-a")
-- Auto-select first item triggers cd
local found_cd = false
for _, cmd in ipairs(mock_state.cmds) do
  if cmd:find("cd ") then
    found_cd = true
    break
  end
end
assert_truthy(found_cd, "switch_project changes directory on selection")

-- Test: switch_project notifies when no projects
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
set_mock_response("/projects", { stdout = {} })
pogo.switch_project()
local found_no_projects = false
for _, n in ipairs(mock_state.notifications) do
  if n.msg:find("no projects") then
    found_no_projects = true
    break
  end
end
assert_truthy(found_no_projects, "switch_project notifies when no projects found")

-- Test: search populates quickfix list (non-telescope path)
clear_mocks()
pogo_client._search_plugin = nil
set_mock_response("/health", { stdout = "ok" })
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
set_mock_response("/plugins", { stdout = { "pogo-plugin-search" } })
local search_result_for_qf = {
  results = {
    files = {
      {
        path = "/src/main.go",
        matches = {
          { line = 10, content = "func main() {" },
          { line = 20, content = "  main()" },
        }
      },
      {
        path = "/src/util.go",
        matches = {
          { line = 5, content = "// used by main" },
        }
      },
    }
  }
}
local enc_val = vim.json.encode(search_result_for_qf)
enc_val = enc_val:gsub("([^%w%-%.%_%~])", function(c)
  return string.format("%%%02X", string.byte(c))
end)
set_mock_response("/plugin", { stdout = { value = enc_val } })
pogo.search("main")
assert_table_len(3, mock_state.qflist, "search populates quickfix with 3 entries")
assert_eq(10, mock_state.qflist[1].lnum, "search quickfix first entry line number")
assert_eq("/test/project/src/main.go", mock_state.qflist[1].filename, "search quickfix first entry filename")
assert_eq(5, mock_state.qflist[3].lnum, "search quickfix third entry line number")
-- Verify copen was called
local found_copen = false
for _, cmd in ipairs(mock_state.cmds) do
  if cmd == "copen" then
    found_copen = true
    break
  end
end
assert_truthy(found_copen, "search opens quickfix window")

-- Test: search notifies on no results
clear_mocks()
pogo_client._search_plugin = "pogo-plugin-search"
set_mock_response("/health", { stdout = "ok" })
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
local empty_result = { results = { files = {} } }
local enc_empty = vim.json.encode(empty_result)
enc_empty = enc_empty:gsub("([^%w%-%.%_%~])", function(c)
  return string.format("%%%02X", string.byte(c))
end)
set_mock_response("/plugin", { stdout = { value = enc_empty } })
pogo.search("nonexistent")
local found_no_results = false
for _, n in ipairs(mock_state.notifications) do
  if n.msg:find("no results") then
    found_no_results = true
    break
  end
end
assert_truthy(found_no_results, "search notifies when no results found")

-- Test: find_file calls vim.ui.select with file list (non-telescope path)
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
set_mock_response("/projects/file", {
  stdout = { paths = { "/src/main.go", "/src/util.go" } }
})
pogo.find_file()
assert_table_len(1, mock_state.ui_select_calls, "find_file calls vim.ui.select")
assert_table_len(2, mock_state.ui_select_calls[1].items, "find_file offers 2 files")

-- Test: find_file notifies when not in a project
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
-- No /file response = visit returns nil
pogo.find_file()
local found_not_in_project = false
for _, n in ipairs(mock_state.notifications) do
  if n.msg:find("not in a project") then
    found_not_in_project = true
    break
  end
end
assert_truthy(found_not_in_project, "find_file notifies when not in a project")

print("")

-------------------------------------------------------------------------------
-- Test Suite 3: Statusline component
-------------------------------------------------------------------------------

print("--- Statusline ---")

-- Test: statusline returns project name with ready icon
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
set_mock_response("/status", {
  stdout = {
    { id = 1, path = "/test/project", indexing_status = "ready", file_count = 42 },
  }
})
-- Force cache expiry: return a time far in the future so (now - cache.time) > ttl
local old_time = os.time
local fake_time = 999999
os.time = function() return fake_time end
local sl = pogo.statusline()
os.time = old_time
assert_eq("pogo[project ✓]", sl, "statusline shows project name with ready checkmark")

-- Test: statusline shows indexing icon
clear_mocks()
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
set_mock_response("/status", {
  stdout = {
    { id = 1, path = "/test/project", indexing_status = "indexing", file_count = 10 },
  }
})
fake_time = fake_time + 999999
os.time = function() return fake_time end
sl = pogo.statusline()
os.time = old_time
assert_eq("pogo[project ⟳]", sl, "statusline shows indexing spinner icon")

-- Test: statusline shows stale icon
clear_mocks()
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
set_mock_response("/status", {
  stdout = {
    { id = 1, path = "/test/project", indexing_status = "stale", file_count = 30 },
  }
})
fake_time = fake_time + 999999
os.time = function() return fake_time end
sl = pogo.statusline()
os.time = old_time
assert_eq("pogo[project !]", sl, "statusline shows stale bang icon")

-- Test: statusline shows unindexed icon
clear_mocks()
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
set_mock_response("/status", {
  stdout = {
    { id = 1, path = "/test/project", indexing_status = "unindexed", file_count = 0 },
  }
})
fake_time = fake_time + 999999
os.time = function() return fake_time end
sl = pogo.statusline()
os.time = old_time
assert_eq("pogo[project ?]", sl, "statusline shows unindexed question mark icon")

-- Test: statusline returns empty when not in a project
clear_mocks()
-- Override expand to return empty (no file open)
local orig_expand = vim.fn.expand
vim.fn.expand = function(expr)
  if expr == "%:p:h" then return "" end
  return orig_expand(expr)
end
vim.fn.getcwd = function() return "" end
-- project_root will fail since visit returns nil
fake_time = fake_time + 999999
os.time = function() return fake_time end
sl = pogo.statusline()
os.time = old_time
vim.fn.expand = orig_expand
vim.fn.getcwd = function() return "/test/project" end
assert_eq("", sl, "statusline returns empty when not in a project")

-- Test: statusline shows bare name when project not in status output
clear_mocks()
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
set_mock_response("/status", {
  stdout = {
    { id = 2, path = "/other/project", indexing_status = "ready", file_count = 10 },
  }
})
fake_time = fake_time + 999999
os.time = function() return fake_time end
sl = pogo.statusline()
os.time = old_time
assert_eq("pogo[project]", sl, "statusline shows bare name when project not in status list")

-- Test: statusline shows bare name when status endpoint fails
clear_mocks()
set_mock_response("/file", {
  stdout = { project = { id = 1, path = "/test/project" } }
})
-- No /status response = returns empty
fake_time = fake_time + 999999
os.time = function() return fake_time end
sl = pogo.statusline()
os.time = old_time
assert_eq("pogo[project]", sl, "statusline shows bare name when status endpoint fails")

print("")

-------------------------------------------------------------------------------
-- Test Suite 4: Status display
-------------------------------------------------------------------------------

print("--- Status display ---")

-- Test: status() shows project statuses via notification (non-telescope path)
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
set_mock_response("/status", {
  stdout = {
    { id = 1, path = "/proj-a", indexing_status = "ready", file_count = 42 },
    { id = 2, path = "/proj-b", indexing_status = "indexing", file_count = 10 },
  }
})
pogo.status()
assert_truthy(#mock_state.notifications > 0, "status() sends notification")
local status_msg = mock_state.notifications[#mock_state.notifications].msg
assert_truthy(status_msg:find("✓"), "status notification contains ready icon")
assert_truthy(status_msg:find("⟳"), "status notification contains indexing icon")
assert_truthy(status_msg:find("/proj%-a"), "status notification contains project-a path")
assert_truthy(status_msg:find("42 files"), "status notification contains file count")

-- Test: status() notifies when no statuses available
clear_mocks()
set_mock_response("/health", { stdout = "ok" })
set_mock_response("/status", { stdout = {} })
pogo.status()
local found_no_status = false
for _, n in ipairs(mock_state.notifications) do
  if n.msg:find("no project status") then
    found_no_status = true
    break
  end
end
assert_truthy(found_no_status, "status() notifies when no status available")

print("")

-------------------------------------------------------------------------------
-- Test Suite 5: project_name helper
-------------------------------------------------------------------------------

print("--- project_name ---")

assert_eq("myrepo", pogo.project_name("/home/user/myrepo"), "project_name extracts dir name")
assert_eq("myrepo", pogo.project_name("/home/user/myrepo/"), "project_name handles trailing slash")
assert_eq("-", pogo.project_name(nil), "project_name returns '-' for nil when no project root")

print("")

-------------------------------------------------------------------------------
-- Summary
-------------------------------------------------------------------------------

print(string.format("=== Results: %d passed, %d failed ===", pass_count, fail_count))
if fail_count > 0 then
  print("")
  print("Failures:")
  for _, e in ipairs(errors) do
    print("  " .. e)
  end
  os.exit(1)
end
