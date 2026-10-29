package main

import (
	"encoding/json"
	"log"
	"os"

	"reproq-worker/internal/protocol"
)

func runCapabilities() {
	if err := json.NewEncoder(os.Stdout).Encode(protocol.Capabilities{WorkerVersion: Version, SchemaID: protocol.SchemaID, SchemaVersion: protocol.SchemaVersion, ExecutorProtocol: protocol.ExecutorVersion}); err != nil {
		log.Fatal(err)
	}
}
