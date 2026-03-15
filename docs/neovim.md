# Neovim Integration

A Neovim plugin (Lua) that integrates with the pogo daemon via its HTTP API, mirroring the capabilities of the Emacs integration.

## Features

- **Project switching** via Telescope or fzf-lua
- **File finding** within the current project
- **Code search** across all indexed projects using zoekt
- **Auto-registration** of projects on `DirChanged`
- **Statusline** component showing current project and indexing status

## Installation

### lazy.nvim

```lua
{
    'drellem2/pogo',
    config = function()
        require('pogo').setup({
            server_url = 'http://localhost:10000',
            auto_visit = true,
        })
    end,
}
```

### packer.nvim

```lua
use {
    'drellem2/pogo',
    config = function()
        require('pogo').setup({
            server_url = 'http://localhost:10000',
            auto_visit = true,
        })
    end,
}
```

## Requirements

- Neovim 0.8+
- [telescope.nvim](https://github.com/nvim-telescope/telescope.nvim) (for project picker and search UI)
- pogo server running (`pogo server start`)

## Configuration

```lua
require('pogo').setup({
    server_url = 'http://localhost:10000',
    auto_visit = true,  -- register projects on DirChanged
})
```

Check the [GitHub repository](https://github.com/drellem2/pogo) for updates on integration availability and status.
