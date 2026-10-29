// Package protocol describes the worker/SDK compatibility boundary. It contains
// no DDL or task serializer; Django migrations own the physical schema.
package protocol

const SchemaVersion = 2
const SchemaID = "reproq-schema-v2"
const SchemaMigration = "0003_periodic_enqueue_v1"
const ExecutorVersion = 2
const PeriodicFunctionBodySHA256 = "eca65eaf6d1dbb5e4afc256f950e3049b6f88439f1be9abd3725096c02f4be8b"

type Capabilities struct {
	WorkerVersion    string         `json:"worker_version,omitempty"`
	SDKVersion       string         `json:"sdk_version,omitempty"`
	SchemaID         string         `json:"schema_id"`
	SchemaVersion    int            `json:"schema_version"`
	ExecutorProtocol int            `json:"executor_protocol"`
	Operations       map[string]int `json:"operations,omitempty"`
}
