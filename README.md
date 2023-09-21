# pogo
code intelligence daemon

*Like a language server for project navigation.*

Goal: Open a file in emacs, then see its repository automatically imported into your favorite IDE.

## Installation
- Mac
`brew install go && chmod +x run.sh && ./run.sh`

See https://github.com/drellem/pogo-mode for an emacs client.

## Environment Variables

- `POGO_HOME`: Folder for pogo to store indexes.
- `POGO_PLUGIN_PATH`: Folder to discover plugins.

## Plugins
You can write/install plugins to provide IDE-like features for all editors. See the included `search` plugin for an example.
