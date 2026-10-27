// Package contracts embeds the established, structural contracts every project
// built on this engine shares: the contract-file format, the core manifest and
// health contracts, the wire envelope, and the configuration schema.
// Domain contracts (yours) are simply more files in this folder.
package contracts

import "embed"

//go:embed contract.schema.json */v*/*.contract.json */v*/*.schema.json
var FS embed.FS
