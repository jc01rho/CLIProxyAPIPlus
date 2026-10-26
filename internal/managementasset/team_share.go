package managementasset

import _ "embed"

//go:embed team_share.html
var teamShareHTML []byte

// TeamShareHTML returns the self-contained team-share overview page.
func TeamShareHTML() []byte { return teamShareHTML }
