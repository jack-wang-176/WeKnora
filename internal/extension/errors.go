package extension

import "errors"

var (
	ErrNotFound        = errors.New("extension: not found")
	ErrNotConfigured   = errors.New("extension: endpoint not configured")
	ErrIncompatible    = errors.New("extension: incompatible host version")
	ErrNotConnected    = errors.New("extension: not connected")
	ErrReservedID      = errors.New("extension: id collides with builtin connector")
	ErrInvalidManifest = errors.New("extension: invalid manifest")
	ErrNotServed       = errors.New("extension: such plugin type is not served")
)
