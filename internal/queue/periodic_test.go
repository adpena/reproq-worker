package queue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSchedulerUsesSharedSDKPortableCronContract(t *testing.T) {
	var contract struct {
		Version int      `json:"schema_version"`
		Grammar string   `json:"grammar"`
		Accept  []string `json:"accepted"`
		Reject  []string `json:"rejected"`
	}
	if err := json.Unmarshal([]byte(readContractFile(t, "cron_schedules.json")), &contract); err != nil || contract.Version != 1 || contract.Grammar != "portable-cron-v1" || len(contract.Accept) == 0 || len(contract.Reject) == 0 {
		t.Fatal("shared SDK cron contract is incomplete")
	}
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	for _, expression := range contract.Accept {
		if _, err := nextPeriodicRun(expression, now); err != nil {
			t.Fatalf("SDK-supported schedule refused: %q: %v", expression, err)
		}
	}
	for _, expression := range contract.Reject {
		if _, err := nextPeriodicRun(expression, now); err == nil {
			t.Fatalf("SDK-unsupported schedule accepted: %q", expression)
		}
	}
}

func TestNamedCronZoneKeepsMidnightAcrossDaylightSavingChange(t *testing.T) {
	expression := "CRON_TZ=America/Chicago 0 0 * * *"
	first, err := nextPeriodicRun(expression, time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("incorrect pre-transition midnight: %v", first)
	}
	second, err := nextPeriodicRun(expression, first)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Equal(time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)) || second.Sub(first) != 25*time.Hour {
		t.Fatalf("named timezone lost daylight-saving transition: %v", second)
	}
}

func TestUnzonedCronUsesUTCAndTruncatedZoneIsRejected(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.FixedZone("fixture", -6*60*60))
	next, err := nextPeriodicRun("0 0 * * *", now)
	if err != nil || !next.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unzoned schedule inherited a connection timezone: %v %v", next, err)
	}
	for _, expression := range []string{"TZ=UTC", "CRON_TZ=America/Chicago"} {
		if _, err := nextPeriodicRun(expression, now); err == nil {
			t.Fatal("truncated timezone prefix accepted")
		}
	}
}

func TestPortableCronRejectsUpstreamExtensionsAndNormalizesTimezoneWhitespace(t *testing.T) {
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	for _, expression := range []string{"? * * * *", "+1 * * * *", "*/61 * * * *", "* */25 * * *", "0 0 * * 7", "0 0 * * FRI-MON", "0 0 L * *", "0 0 * * MON#2", "@daily"} {
		if _, err := nextPeriodicRun(expression, now); err == nil {
			t.Fatalf("unsupported cron accepted: %q", expression)
		}
	}
	if next, err := nextPeriodicRun("CRON_TZ=UTC\t0\t0 * * *", now); err != nil || !next.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("portable whitespace changed timezone expression: %v %v", next, err)
	}
}
