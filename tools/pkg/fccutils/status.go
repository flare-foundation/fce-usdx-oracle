package fccutils

import "fmt"

// The registry's machine statuses, in the order of upstream's
// IMachineManager.TeeStatus enum.
//
// A registered machine starts INITIALIZED and reaches PRODUCTION through
// toProduction with an availability proof; the feed store accepts signatures
// from no other status. Its owner can pause a PRODUCTION or SUSPENDED machine,
// and anyone can suspend a PRODUCTION machine whose availability check has
// expired; both leave the active set. toProduction with a fresh availability
// proof brings either back: anyone may call it for a SUSPENDED machine, only
// the owner for a PAUSED one. BANNED is the extension owner's, and unban
// returns the machine to PAUSED.
const (
	TeeStatusNone        uint8 = 0
	TeeStatusInitialized uint8 = 1
	TeeStatusProduction  uint8 = 2
	TeeStatusSuspended   uint8 = 3
	TeeStatusPaused      uint8 = 4
	TeeStatusBanned      uint8 = 5
)

// TeeStatusName renders a status as upstream names it, or UNKNOWN(n) for a
// value the enum does not have.
func TeeStatusName(status uint8) string {
	switch status {
	case TeeStatusNone:
		return "NONE"
	case TeeStatusInitialized:
		return "INITIALIZED"
	case TeeStatusProduction:
		return "PRODUCTION"
	case TeeStatusSuspended:
		return "SUSPENDED"
	case TeeStatusPaused:
		return "PAUSED"
	case TeeStatusBanned:
		return "BANNED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", status)
	}
}
