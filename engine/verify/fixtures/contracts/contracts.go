// Package contracts (TEST FIXTURE) embeds sample domain contracts used only by the verification suite. The JSON files
// are the source of truth; any language reads them. Go structs elsewhere in
// this layer are just a convenience and are verified against these files.
package contracts

import "embed"

//go:embed */v*/*.contract.json
var FS embed.FS
