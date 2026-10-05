# No param block: collect native arguments including flags, Unicode and empty strings.
@{ host = $env:OLLAMA_HOST; models = $env:OLLAMA_MODELS; arguments = @($args) } | ConvertTo-Json -Compress
exit 7
