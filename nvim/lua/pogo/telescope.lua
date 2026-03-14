-- pogo/telescope.lua - Telescope pickers for pogo
local M = {}

local client = require("pogo.client")

local has_telescope, _ = pcall(require, "telescope")
if not has_telescope then
  return M
end

local pickers = require("telescope.pickers")
local finders = require("telescope.finders")
local conf = require("telescope.config").values
local actions = require("telescope.actions")
local action_state = require("telescope.actions.state")
local entry_display = require("telescope.pickers.entry_display")
local previewers = require("telescope.previewers")

--- Telescope picker: switch to a known project
function M.projects(opts)
  opts = opts or {}
  if not client.ensure_server() then return end

  local projects = client.get_projects()
  if #projects == 0 then
    vim.notify("[pogo] no projects found", vim.log.levels.INFO)
    return
  end

  local paths = {}
  for _, proj in ipairs(projects) do
    table.insert(paths, proj.path)
  end

  pickers.new(opts, {
    prompt_title = "Pogo Projects",
    finder = finders.new_table({
      results = paths,
    }),
    sorter = conf.generic_sorter(opts),
    attach_mappings = function(prompt_bufnr, _)
      actions.select_default:replace(function()
        actions.close(prompt_bufnr)
        local selection = action_state.get_selected_entry()
        if selection then
          vim.cmd("cd " .. vim.fn.fnameescape(selection[1]))
          vim.notify("[pogo] switched to " .. selection[1])
        end
      end)
      return true
    end,
  }):find()
end

--- Telescope picker: find file in current project
function M.find_file(opts)
  opts = opts or {}
  if not client.ensure_server() then return end

  local project_root = client.project_root(vim.fn.getcwd())
  if not project_root then
    vim.notify("[pogo] not in a project", vim.log.levels.WARN)
    return
  end

  local files = client.get_project_files(project_root)
  if #files == 0 then
    vim.notify("[pogo] no files found (index may be building)", vim.log.levels.INFO)
    return
  end

  pickers.new(opts, {
    prompt_title = "Pogo Find File [" .. vim.fn.fnamemodify(project_root, ":t") .. "]",
    finder = finders.new_table({
      results = files,
    }),
    sorter = conf.file_sorter(opts),
    attach_mappings = function(prompt_bufnr, _)
      actions.select_default:replace(function()
        actions.close(prompt_bufnr)
        local selection = action_state.get_selected_entry()
        if selection then
          local full_path = project_root .. selection[1]
          vim.cmd("edit " .. vim.fn.fnameescape(full_path))
        end
      end)
      return true
    end,
  }):find()
end

--- Telescope picker: search project code
function M.search(opts)
  opts = opts or {}
  if not client.ensure_server() then return end

  local project_root = client.project_root(vim.fn.getcwd())
  if not project_root then
    vim.notify("[pogo] not in a project", vim.log.levels.WARN)
    return
  end

  -- Get query from opts or prompt
  local query = opts.query
  if not query then
    query = vim.fn.input("Zoekt query: ", vim.fn.expand("<cword>"))
    if query == "" then return end
  end

  local resp = client.search(project_root, query)
  if not resp then
    vim.notify("[pogo] search failed", vim.log.levels.ERROR)
    return
  end
  if resp.error and resp.error ~= "" then
    vim.notify("[pogo] search error: " .. resp.error, vim.log.levels.ERROR)
    return
  end

  local results = {}
  if resp.results and resp.results.files then
    for _, file in ipairs(resp.results.files) do
      for _, match in ipairs(file.matches or {}) do
        table.insert(results, {
          path = file.path,
          line = match.line,
          content = match.content,
          full_path = project_root .. file.path,
        })
      end
    end
  end

  if #results == 0 then
    vim.notify("[pogo] no results for: " .. query, vim.log.levels.INFO)
    return
  end

  local displayer = entry_display.create({
    separator = " ",
    items = {
      { width = 40 },
      { width = 6 },
      { remaining = true },
    },
  })

  pickers.new(opts, {
    prompt_title = "Pogo Search: " .. query,
    finder = finders.new_table({
      results = results,
      entry_maker = function(entry)
        return {
          value = entry,
          display = function(e)
            return displayer({
              e.value.path,
              { tostring(e.value.line), "TelescopeResultsLineNr" },
              { vim.trim(e.value.content), "TelescopeResultsComment" },
            })
          end,
          ordinal = entry.path .. " " .. entry.content,
          filename = entry.full_path,
          lnum = entry.line,
        }
      end,
    }),
    sorter = conf.generic_sorter(opts),
    previewer = conf.grep_previewer(opts),
  }):find()
end

--- Telescope picker: show project status
function M.status(opts)
  opts = opts or {}
  if not client.ensure_server() then return end

  local statuses = client.get_status()
  if #statuses == 0 then
    vim.notify("[pogo] no project status available", vim.log.levels.INFO)
    return
  end

  local status_icons = {
    ready = "✓",
    indexing = "⟳",
    stale = "!",
    unindexed = "?",
  }

  local displayer = entry_display.create({
    separator = " ",
    items = {
      { width = 3 },
      { width = 8 },
      { remaining = true },
    },
  })

  pickers.new(opts, {
    prompt_title = "Pogo Project Status",
    finder = finders.new_table({
      results = statuses,
      entry_maker = function(entry)
        local icon = status_icons[entry.indexing_status] or "?"
        return {
          value = entry,
          display = function(e)
            return displayer({
              icon,
              { tostring(e.value.file_count) .. " files", "TelescopeResultsNumber" },
              e.value.path,
            })
          end,
          ordinal = entry.path,
        }
      end,
    }),
    sorter = conf.generic_sorter(opts),
    attach_mappings = function(prompt_bufnr, _)
      actions.select_default:replace(function()
        actions.close(prompt_bufnr)
        local selection = action_state.get_selected_entry()
        if selection then
          vim.cmd("cd " .. vim.fn.fnameescape(selection.value.path))
          vim.notify("[pogo] switched to " .. selection.value.path)
        end
      end)
      return true
    end,
  }):find()
end

return M
