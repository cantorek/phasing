package scripts

import (
	_ "embed"
)

//go:embed init.sh
var InitScript string

//go:embed phasing.yaml
var PhasingYAML string
