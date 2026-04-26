package protocol

import "time"

const (
	MTLSValidity         = 10 * 365 * 24 * time.Hour
	MTLSOrg              = "mcptp"
	MTLSCACommonName     = "mcptp-CA"
	MTLSServerCommonName = "mcptp-server"
	MTLSClientCommonName = "mcptp-client"
)
