let s:skip_once = {}

function! cc#session#detect_buffer() abort
  let l:path = expand('%:p')
  if empty(l:path) || !filereadable(l:path) || &l:buftype !=# ''
    return
  endif
  if s:consume_skip(l:path)
    return
  endif
  if s:is_session_lines(l:path, getline(1, min([line('$'), 5])))
    setlocal filetype=ccsession
  endif
endfunction

function! cc#session#is_session_file(path) abort
  if empty(a:path) || !filereadable(a:path)
    return 0
  endif
  return s:is_session_lines(a:path, readfile(a:path, '', 5))
endfunction

function! cc#session#render_current(force) abort
  let l:source = s:source_path()
  if empty(l:source) || !cc#session#is_session_file(l:source)
    return 0
  endif
  if !a:force && get(b:, 'cc_session_rendered', 0)
    return 1
  endif

  let l:cmd = s:render_command(l:source)
  if empty(l:cmd)
    call s:error('ccfmt not found; set g:cc_session_formatter')
    return 0
  endif

  let l:lines = systemlist(l:cmd)
  if v:shell_error != 0
    call s:error('ccfmt failed: ' . join(l:lines, ' '))
    return 0
  endif

  let l:view = winsaveview()
  let b:cc_session_source = l:source
  let b:cc_session_rendered = 1

  setlocal modifiable noreadonly
  silent %delete _
  if empty(l:lines)
    call setline(1, [''])
  else
    call setline(1, l:lines)
  endif

  execute 'silent file ' . fnameescape('[cc] ' . fnamemodify(l:source, ':t'))
  setlocal filetype=markdown
  call s:apply_render_options()
  call winrestview(l:view)
  return 1
endfunction

function! cc#session#edit_raw() abort
  let l:source = s:source_path()
  if empty(l:source) || !filereadable(l:source)
    return
  endif

  call s:mark_skip(l:source)
  unlet! b:cc_session_rendered
  unlet! b:cc_session_source
  setlocal buftype= bufhidden= modifiable noreadonly swapfile
  setlocal filetype=
  execute 'edit ' . fnameescape(l:source)
  setlocal filetype=json
endfunction

function! s:source_path() abort
  if exists('b:cc_session_source') && !empty(b:cc_session_source)
    return b:cc_session_source
  endif
  return expand('%:p')
endfunction

function! s:is_session_lines(path, lines) abort
  let l:path = substitute(fnamemodify(a:path, ':p'), '\\', '/', 'g')
  let l:ext = fnamemodify(a:path, ':e')
  if l:ext !=# 'jsonl'
    if l:ext !=# 'json' || l:path !~# '/storage/session/.*/ses_[^/]\+\.json$'
      return 0
    endif
    for l:line in a:lines
      if l:line =~# '"id"\s*:\s*"ses_'
        return 1
      endif
    endfor
    return 0
  endif

  if l:path =~# '/subagents/agent-[^/]\+\.jsonl$'
    return 1
  endif

  for l:line in a:lines
    if empty(l:line)
      continue
    endif
    if l:line =~# '"type"\s*:\s*"session_meta"'
      return 1
    endif
    if l:line =~# '"type"\s*:\s*"response_item"'
      return 1
    endif
    if l:line =~# '"toolUseResult"\s*:'
      return 1
    endif
    if l:line =~# '"timestamp"\s*:'
          \ && l:line =~# '"message"\s*:'
          \ && l:line =~# '"type"\s*:\s*"\(user\|assistant\|system\|summary\|custom-title\)"'
      return 1
    endif
  endfor
  return 0
endfunction

function! s:formatter_command() abort
  if exists('g:cc_session_formatter') && !empty(g:cc_session_formatter)
    return g:cc_session_formatter
  endif
  if executable('ccfmt')
    return 'ccfmt'
  endif

  let l:root = fnamemodify(expand('<sfile>:p'), ':h:h:h:h')
  if executable('go') && filereadable(l:root . '/go.mod') && isdirectory(l:root . '/cmd/ccfmt')
    return 'cd ' . shellescape(l:root) . ' && go run ./cmd/ccfmt'
  endif
  return ''
endfunction

function! s:render_command(path) abort
  let l:formatter = s:formatter_command()
  if empty(l:formatter)
    return ''
  endif
  let l:args = get(g:, 'cc_session_render_args', '-format markdown -cleanup publish')
  return l:formatter . ' ' . l:args . ' ' . shellescape(a:path) . ' 2>&1'
endfunction

function! s:apply_render_options() abort
  setlocal buftype=nofile bufhidden=hide noswapfile
  setlocal nomodifiable readonly nomodified
  setlocal conceallevel=0
endfunction

function! s:mark_skip(path) abort
  let s:skip_once[fnamemodify(a:path, ':p')] = 1
endfunction

function! s:consume_skip(path) abort
  let l:path = fnamemodify(a:path, ':p')
  if has_key(s:skip_once, l:path)
    call remove(s:skip_once, l:path)
    return 1
  endif
  return 0
endfunction

function! s:error(msg) abort
  echohl ErrorMsg
  echomsg 'cc: ' . a:msg
  echohl None
endfunction
