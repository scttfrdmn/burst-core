package protocol_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// --- SessionStatus JSON round-trip ---

func TestSessionStatusJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	s := protocol.SessionStatus{
		SessionID:      "py-20260326-a1b2c3d4",
		Language:       "python",
		Status:         protocol.StatusRunning,
		TasksTotal:     100,
		TasksComplete:  42,
		TasksFailed:    2,
		WorkersActive:  10,
		ElapsedSeconds: 15.3,
		CostActual:     0.12,
		CostEstimate:   2.80,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got protocol.SessionStatus
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SessionID != s.SessionID {
		t.Errorf("session_id: got %q, want %q", got.SessionID, s.SessionID)
	}
	if got.TasksTotal != s.TasksTotal {
		t.Errorf("tasks_total: got %d, want %d", got.TasksTotal, s.TasksTotal)
	}
	if got.CostEstimate != s.CostEstimate {
		t.Errorf("cost_estimate_per_hour: got %v, want %v", got.CostEstimate, s.CostEstimate)
	}
}

func TestSessionStatusJSONFieldNames(t *testing.T) {
	s := protocol.SessionStatus{
		SessionID: "go-20260326-deadbeef",
		Language:  "go",
		Status:    protocol.StatusInitializing,
	}
	b, _ := json.Marshal(s)
	raw := string(b)

	for _, field := range []string{
		`"session_id"`, `"language"`, `"status"`,
		`"tasks_total"`, `"tasks_complete"`, `"tasks_failed"`,
		`"workers_active"`, `"elapsed_seconds"`, `"cost_actual"`,
		`"cost_estimate_per_hour"`, `"created_at"`, `"updated_at"`,
	} {
		if !strings.Contains(raw, field) {
			t.Errorf("JSON missing field %s", field)
		}
	}
}

// --- Manifest JSON round-trip ---

func TestManifestEmbedsSessionStatus(t *testing.T) {
	m := protocol.Manifest{
		SessionStatus: protocol.SessionStatus{
			SessionID: "jl-20260326-cafebabe",
			Language:  "julia",
			Status:    protocol.StatusInitializing,
		},
		EnvHash:          "sha256:abc",
		LibraryVersion:   "0.1.0",
		WorkersRequested: 50,
		WorkersActual:    50,
		CPU:              4,
		MemoryGB:         8,
		Backend:          "fargate",
		Region:           "us-east-1",
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got protocol.Manifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SessionID != m.SessionID {
		t.Errorf("embedded session_id: got %q want %q", got.SessionID, m.SessionID)
	}
	if got.CPU != m.CPU {
		t.Errorf("cpu: got %d want %d", got.CPU, m.CPU)
	}
}

// --- Error types ---

func TestBurstPartialError(t *testing.T) {
	e := &protocol.BurstPartialError{FailedCount: 3, SuccessCount: 97}
	msg := e.Error()
	if !strings.Contains(msg, "3/100") {
		t.Errorf("BurstPartialError.Error() = %q, want to contain \"3/100\"", msg)
	}
}

func TestBurstQuotaError(t *testing.T) {
	e := &protocol.BurstQuotaError{
		RequestedWorkers: 100,
		ActualWorkers:    16,
		QuotaName:        "Fargate On-Demand vCPU",
		QuotaValue:       32,
	}
	msg := e.Error()
	if !strings.Contains(msg, "16") {
		t.Errorf("BurstQuotaError.Error() = %q, want to contain \"16\"", msg)
	}
}

func TestBurstCostLimitError(t *testing.T) {
	e := &protocol.BurstCostLimitError{Limit: 10.0, EstimatedCost: 25.50}
	msg := e.Error()
	if !strings.Contains(msg, "25.50") {
		t.Errorf("BurstCostLimitError.Error() = %q, want to contain \"25.50\"", msg)
	}
	if !strings.Contains(msg, "10.00") {
		t.Errorf("BurstCostLimitError.Error() = %q, want to contain \"10.00\"", msg)
	}
}

func TestBurstTimeoutError(t *testing.T) {
	e := &protocol.BurstTimeoutError{
		SessionID: "py-20260326-a1b2c3d4",
		Status: &protocol.SessionStatus{
			TasksComplete: 80,
			TasksTotal:    100,
		},
	}
	msg := e.Error()
	if !strings.Contains(msg, "py-20260326-a1b2c3d4") {
		t.Errorf("BurstTimeoutError.Error() = %q, want session ID", msg)
	}
	if !strings.Contains(msg, "80/100") {
		t.Errorf("BurstTimeoutError.Error() = %q, want progress", msg)
	}
}

func TestBurstSetupError(t *testing.T) {
	e := &protocol.BurstSetupError{
		Step:        "create S3 bucket",
		Cause:       "BucketAlreadyOwnedByYou",
		Remediation: "run burst-core teardown first",
	}
	msg := e.Error()
	if !strings.Contains(msg, "create S3 bucket") {
		t.Errorf("BurstSetupError.Error() = %q, want step name", msg)
	}
	if !strings.Contains(msg, "run burst-core teardown first") {
		t.Errorf("BurstSetupError.Error() = %q, want remediation", msg)
	}
}

func TestBurstSetupErrorNoRemediation(t *testing.T) {
	e := &protocol.BurstSetupError{Step: "create cluster", Cause: "AccessDenied"}
	msg := e.Error()
	if strings.Contains(msg, "—") {
		t.Errorf("BurstSetupError with no remediation should not contain separator: %q", msg)
	}
}

// --- Schema / S3 keys ---

func TestS3Keys(t *testing.T) {
	keys := protocol.S3Keys("my-bucket", "py-20260326-a1b2c3d4")

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Manifest", keys.Manifest, "sessions/py-20260326-a1b2c3d4/manifest.json"},
		{"TasksDir", keys.TasksDir, "sessions/py-20260326-a1b2c3d4/tasks/"},
		{"TaskKey", keys.TaskKey("task-0000"), "sessions/py-20260326-a1b2c3d4/tasks/task-0000.task"},
		{"ResultKey", keys.ResultKey("task-0001"), "sessions/py-20260326-a1b2c3d4/tasks/task-0001.result"},
		{"StatusKey", keys.StatusKey("task-0002"), "sessions/py-20260326-a1b2c3d4/tasks/task-0002.status"},
		{"ErrorKey", keys.ErrorKey("task-0003"), "sessions/py-20260326-a1b2c3d4/tasks/task-0003.error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

// --- Session ID generation ---

var sessionIDPattern = regexp.MustCompile(`^[a-z_]{2}-\d{8}-[0-9a-f]{8}$`)

func TestGenerateSessionID(t *testing.T) {
	langs := []string{
		protocol.LangPython,
		protocol.LangJulia,
		protocol.LangTypeScript,
		protocol.LangGo,
		protocol.LangR,
	}

	for _, lang := range langs {
		t.Run(lang, func(t *testing.T) {
			id := protocol.GenerateSessionID(lang)
			if !sessionIDPattern.MatchString(id) {
				t.Errorf("GenerateSessionID(%q) = %q, does not match pattern %s",
					lang, id, sessionIDPattern)
			}
			if !strings.HasPrefix(id, lang+"-") {
				t.Errorf("GenerateSessionID(%q) = %q, want prefix %q", lang, id, lang+"-")
			}
		})
	}
}

func TestGenerateSessionIDUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := protocol.GenerateSessionID(protocol.LangPython)
		if seen[id] {
			t.Errorf("duplicate session ID generated: %q", id)
		}
		seen[id] = true
	}
}

// --- TaskID ---

func TestTaskID(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "task-0000"},
		{1, "task-0001"},
		{9, "task-0009"},
		{99, "task-0099"},
		{999, "task-0999"},
		{9999, "task-9999"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("n=%d", tt.n), func(t *testing.T) {
			got := protocol.TaskID(tt.n)
			if got != tt.want {
				t.Errorf("TaskID(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}
