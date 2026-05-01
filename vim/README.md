# Vim Support

The runtime under `vim/` turns cc session `.jsonl` files into readable
markdown transcripts when you open them in Vim or Neovim.

## Install

Add the repo runtime to `runtimepath`:

```vim
set runtimepath+=/path/to/cc/vim
```

The plugin looks for `ccfmt` on `$PATH`. If it is not installed, it falls back
to `go run ./cmd/ccfmt` from this repo checkout.

## Commands

- `:CCSessionRender` renders the current session buffer through `ccfmt`.
- `:CCSessionRefresh` rerenders from the source `.jsonl`.
- `:CCSessionRaw` reopens the underlying JSONL with `filetype=json`.

## Settings

- `g:cc_session_auto_render` defaults to `1`.
- `g:cc_session_formatter` overrides the formatter command.
- `g:cc_session_render_args` defaults to `-format markdown -cleanup publish`.
