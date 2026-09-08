package extension

import "errors"

var (
	ErrNotFound           = errors.New("extension: not found")
	ErrNotConfigured      = errors.New("extension: endpoint not configured")
	ErrIncompatible       = errors.New("extension: incompatible host version")
	ErrNotConnected       = errors.New("extension: not connected")
	ErrReservedID         = errors.New("extension: id collides with builtin connector")
	ErrInvalidManifest    = errors.New("extension: invalid manifest")
	ErrNotServed          = errors.New("extension: such plugin type is not served")
	ErrPluginRepeated     = errors.New("extension: builtin plugin already registered")
	ErrPluginUnregistered = errors.New("extension: plugin not registered")
	ErrNotCompatible      = errors.New("extension: capability host is not compatible")
	ErrUnauthorizedEnv    = errors.New("extension: plugin env is not authorized")
	ErrEndpointImmutable  = errors.New("extension: channel does not accept an endpoint change")
	ErrInvalidAddr        = errors.New("extension: addr is invalid")
	ErrNoLoader           = errors.New("extension: host has no manifest loader")
	ErrBuiltinImmutable   = errors.New("extension: builtin manifest is immutable")
	// ErrUnenforceable says "you wrote it correctly, but this host cannot
	// enforce it". It is deliberately distinct from ErrInvalidManifest: a
	// plugin author reacts to it by dropping the declaration, not by fixing
	// syntax. A permission the host does not enforce is worse than an absent
	// one, because the UI shows it to the user as a fact.
	ErrUnenforceable = errors.New("extension: permission cannot be enforced by this host")
)
