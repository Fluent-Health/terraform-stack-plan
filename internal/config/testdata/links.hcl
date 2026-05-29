links {
  resource = "https://gh/o/r/blob/{sha}/{file}#L{line}"
  stack    = "https://gh/o/r/tree/{sha}/{stack_dir}"
  header {
    label = "Build #{build_id}"
    url   = "https://cb/{build_id}"
  }
  header {
    label = "PR #{pr}"
    url   = "https://gh/o/r/pull/{pr}"
  }
}
