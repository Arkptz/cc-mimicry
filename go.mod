module github.com/arkptz/cc-mimicry

go 1.26.7

require (
	github.com/router-for-me/CLIProxyAPI/v7 v7.2.157
	github.com/tidwall/gjson v1.18.0
	github.com/tidwall/sjson v1.2.5
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
)

replace github.com/router-for-me/CLIProxyAPI/v7 => ../forks/CLIProxyAPI
