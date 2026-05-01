augroup cc_session_filetype
  autocmd!
  autocmd BufRead *.jsonl call cc#session#detect_buffer()
augroup END
