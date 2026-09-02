package benchmarkfixtures

import "embed"

// FS contains the canonical offline benchmark scenarios.
//
//go:embed v1/*.json
var FS embed.FS
