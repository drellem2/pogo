# Neovim Integration

> **Status**: Under development. This document describes the planned integration.

A Neovim plugin (Lua) that integrates with the pogo daemon via its HTTP API, mirroring the capabilities of the Emacs integration.

## Planned Features

- **Project switching** via Telescope or fzf-lua
- **File finding** within the current project
- **Code search** across all indexed projects using zoekt
- **Auto-registration** of projects on `BufEnter`
- **Statusline** component showing current project and indexing status

## Requirements (expected)

- Neovim 0.8+
- [telescope.nvim](https://github.com/nvim-telescope/telescope.nvim) or [fzf-lua](https://github.com/ibhagwan/fzf-lua)
- pogo server running (`pogo server start`)

## Configuration (expected)

```lua
require('pogo').setup({
    server_url = 'http://localhost:10000',
    auto_visit = true,  -- register projects on BufEnter
})
```

Check the [GitHub repository](https://github.com/drellem2/pogo) for updates on availability.
