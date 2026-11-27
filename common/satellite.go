package common

import (
	"os"
	"strings"
)

// IsSatellite identifies an independently stored, periodically synchronized node.
// NODE_TYPE=slave has different semantics: it shares its primary's database.
func IsSatellite() bool { return os.Getenv("SATELLITE_ROLE") == "satellite" }

func SatelliteNodeID() string { return strings.TrimSpace(os.Getenv("SATELLITE_NODE_ID")) }
