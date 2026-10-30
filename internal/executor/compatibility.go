package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"reproq-worker/internal/config"
	"reproq-worker/internal/protocol"
)

const capabilityResponseLimit = 4096
const capabilityProbeTimeout = 15 * time.Second

func (e *ShellExecutor) CheckCompatibility(ctx context.Context) error {
	if err := config.ValidateExecutionLimits(e.MaxPayloadBytes, e.MaxStdoutBytes, e.MaxStderrBytes, capabilityProbeTimeout); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, capabilityProbeTimeout)
	defer cancel()
	command := exec.CommandContext(bounded, e.PythonBin, "-m", e.ExecutorModule, "--capabilities")
	stdout, _, err := runBoundedProcess(bounded, command, capabilityResponseLimit, capabilityResponseLimit)
	if err != nil {
		return errors.New("python executor capability probe failed; install the compatible ReproQ SDK in the configured Python environment before starting a worker")
	}
	if stdout.truncated {
		return errors.New("python executor capability response exceeded the control-message limit")
	}
	fields, err := parseProtocolObject(stdout.Bytes())
	if err != nil {
		return errors.New("python executor returned invalid capability JSON; upgrade the configured ReproQ SDK")
	}
	var sdk, schema string
	var schemaVersion, executorVersion int
	if json.Unmarshal(fields["sdk_version"], &sdk) != nil || strings.TrimSpace(sdk) == "" ||
		json.Unmarshal(fields["schema_id"], &schema) != nil || schema != protocol.SchemaID ||
		json.Unmarshal(fields["schema_version"], &schemaVersion) != nil || schemaVersion != protocol.SchemaVersion ||
		json.Unmarshal(fields["executor_protocol"], &executorVersion) != nil || executorVersion != protocol.ExecutorVersion {
		return errors.New("python executor schema/protocol is incompatible; install the matching ReproQ SDK before claiming tasks")
	}
	return nil
}
