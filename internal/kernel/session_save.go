package kernel

import "errors"

// ErrSessionStateSuperseded indicates that a session save was discarded
// because a newer state already superseded it.
var ErrSessionStateSuperseded = errors.New("session state superseded")
