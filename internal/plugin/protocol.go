// Package plugin defines the wire protocol types for mu's plugin system.
// Plugins communicate with the build coordinator over NDJSON (newline-delimited JSON).
//
// The wire types themselves live in sdk/muplugin (the public plugin SDK).
// This file re-exports them as type aliases so the rest of the mu codebase
// keeps its existing import paths (plugin.Request, plugin.PlanResponse,
// plugin.NewPlanRequest, etc.) while plugin authors write against the
// public sdk/muplugin package.
package plugin

import "github.com/chau/mu/sdk/muplugin"

// ProtocolVersion is the current version of the plugin protocol.
const ProtocolVersion = muplugin.ProtocolVersion

// Wire type aliases — sdk/muplugin is the source of truth.
type (
	Request               = muplugin.Request
	DiscoverResponse      = muplugin.DiscoverResponse
	SchemaRef             = muplugin.SchemaRef
	ObserveResponse       = muplugin.ObserveResponse
	PlanResponse          = muplugin.PlanResponse
	TargetInfo            = muplugin.TargetInfo
	DepInfo               = muplugin.DepInfo
	ActionSpec            = muplugin.ActionSpec
	ResolveSecretResponse = muplugin.ResolveSecretResponse
	StoreSecretResponse   = muplugin.StoreSecretResponse
	AdviseContext         = muplugin.AdviseContext
	AdviseResponse        = muplugin.AdviseResponse
)

// StoreSecretMode aliases.
const (
	StoreSecretModeCreate         = muplugin.StoreSecretModeCreate
	StoreSecretModeOverwrite      = muplugin.StoreSecretModeOverwrite
	StoreSecretModeCreateIfAbsent = muplugin.StoreSecretModeCreateIfAbsent
)

// DefaultAdviseTimeout is re-exported for callers in internal/.
const DefaultAdviseTimeout = muplugin.DefaultAdviseTimeout

// Constructor re-exports (function values, since Go has no `func` alias).
var (
	NewDiscoverRequest      = muplugin.NewDiscoverRequest
	NewPlanRequest          = muplugin.NewPlanRequest
	NewObserveRequest       = muplugin.NewObserveRequest
	NewResolveSecretRequest = muplugin.NewResolveSecretRequest
	NewStoreSecretRequest   = muplugin.NewStoreSecretRequest
	NewAdviseRequest        = muplugin.NewAdviseRequest
)
