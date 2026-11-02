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

--- Telescope picker: show project status
function M.status(opts)
  opts = opts or {}
  if not client.ensure_server() then return end

  local statuses = client.get_status()
  if #statuses == 0 then
    vim.notify("[pogo] no project status available", vim.log.levels.INFO)
    return
  end

  pickers.new(opts, {
    prompt_title = "Pogo Project Status",
    finder = finders.new_table({
      results = statuses,
      entry_maker = function(entry)
        return {
          value = entry,
          display = entry.path,
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
