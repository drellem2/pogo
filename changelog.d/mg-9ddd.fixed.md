- **Internal: a driver test raced the indexer.** `TestPluginExecute` slept for a
  fixed second and then asserted the project was indexed; indexing is asynchronous,
  so when it ran long the test failed and flunked the build gate on unrelated
  merges. It now polls until the project reports ready. Test-only — pogo itself is
  unchanged.
