package domain

import "errors"

// ErrUnsupported is returned by a UIDriver method the underlying backend
// cannot perform (e.g. Navigate on a native desktop driver).
var ErrUnsupported = errors.New("operation not supported by this driver")

// ErrToolNotRegistered is returned by the ToolRegistry when a scenario
// references a tool name that was never registered.
var ErrToolNotRegistered = errors.New("tool not registered")
