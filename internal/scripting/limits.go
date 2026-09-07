package scripting

const (
	DefaultTimeoutSeconds     = 30
	MaxTimeoutSeconds         = 300
	DefaultRunnerMemoryMiB    = 64
	DefaultControllerConcurrency = 2
	DefaultScriptConcurrency  = 1
	DefaultSDKCallLimit       = 100
	DefaultManageActionLimit  = 10
	DefaultHostPowerTargets   = 1
	MaxSourceBytes            = 256 * 1024
	MaxParamsEnvBytes         = 64 * 1024
	MaxResultBytes            = 64 * 1024
	MaxLogBytes               = 256 * 1024
	MaxParamSchemaDepth       = 6
	MaxParamSchemaNodes       = 80
	MaxMetricConditions       = 4
	HostPowerTTLSeconds       = 60
	OperationsWaitMaxSeconds  = 8
	DefaultLogRetentionDays   = 30
	DefaultSummaryRetentionDays = 90
)
