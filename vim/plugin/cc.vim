if exists('g:loaded_cc_vim')
  finish
endif
let g:loaded_cc_vim = 1

command! CCSessionRender call cc#session#render_current(1)
command! CCSessionRefresh call cc#session#render_current(1)
command! CCSessionRaw call cc#session#edit_raw()

augroup cc_session_render
  autocmd!
  autocmd FileType ccsession if get(g:, 'cc_session_auto_render', 1) | call cc#session#render_current(0) | endif
augroup END
