package aji

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// --- Registry tests ---

func TestRegisterAndLookup(t *testing.T) {
	fn := func(x int) (int, error) { return x * 2, nil }
	Register("double", fn)

	name, ok := lookupName(fn)
	if !ok {
		t.Fatal("lookupName returned false for registered function")
	}
	if name != "double" {
		t.Fatalf("expected name %q, got %q", "double", name)
	}

	entry, ok := lookupEntry("double")
	if !ok {
		t.Fatal("lookupEntry returned false for registered function")
	}
	if entry.inputType != reflect.TypeOf(0) {
		t.Fatalf("unexpected inputType: %v", entry.inputType)
	}
}

func TestRegisterSimple(t *testing.T) {
	fn := func(x string) string { return "hello " + x }
	RegisterSimple("greet", fn)

	entry, ok := lookupEntry("greet")
	if !ok {
		t.Fatal("lookupEntry returned false for registered simple function")
	}
	result, err := callRegistered(entry, "world")
	if err != nil {
		t.Fatalf("callRegistered error: %v", err)
	}
	if result.(string) != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", result)
	}
}

func TestRegisteredFnName_unregistered(t *testing.T) {
	fn := func(x float64) (float64, error) { return x, nil }
	_, err := registeredFnName(fn)
	if err == nil {
		t.Fatal("expected error for unregistered function")
	}
}

// --- Serialize tests ---

func TestSerializeRoundtrip(t *testing.T) {
	Register("add1", func(x int) (int, error) { return x + 1, nil })

	items := []int{1, 2, 3}
	data, err := SerializeTask("add1", 0, items)
	if err != nil {
		t.Fatalf("SerializeTask error: %v", err)
	}

	payload, err := DeserializeTask(data)
	if err != nil {
		t.Fatalf("DeserializeTask error: %v", err)
	}
	if payload.FunctionName != "add1" {
		t.Fatalf("expected function %q, got %q", "add1", payload.FunctionName)
	}
	if payload.ChunkIndex != 0 {
		t.Fatalf("expected chunk_index 0, got %d", payload.ChunkIndex)
	}

	entry, _ := lookupEntry("add1")
	deserialized, err := DeserializeItems(payload.Items, entry.inputType)
	if err != nil {
		t.Fatalf("DeserializeItems error: %v", err)
	}
	if len(deserialized) != 3 {
		t.Fatalf("expected 3 items, got %d", len(deserialized))
	}
	if deserialized[0].(int) != 1 {
		t.Fatalf("expected item[0]=1, got %v", deserialized[0])
	}

	results := []any{2, 3, 4}
	errs := []string{"", "", ""}
	resultData, err := SerializeResult(results, errs)
	if err != nil {
		t.Fatalf("SerializeResult error: %v", err)
	}

	rp, err := DeserializeResult(resultData)
	if err != nil {
		t.Fatalf("DeserializeResult error: %v", err)
	}
	if len(rp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(rp.Results))
	}
	var v int
	if err := json.Unmarshal(rp.Results[0], &v); err != nil {
		t.Fatalf("unmarshal result[0]: %v", err)
	}
	if v != 2 {
		t.Fatalf("expected result[0]=2, got %d", v)
	}
}

// --- IsWorkerMode tests ---

func TestIsWorkerMode_arg(t *testing.T) {
	orig := os.Args
	defer func() { os.Args = orig }()
	os.Args = []string{"test", "--aji-worker"}
	if !IsWorkerMode() {
		t.Fatal("expected IsWorkerMode()=true with --aji-worker arg")
	}
}

func TestIsWorkerMode_env(t *testing.T) {
	t.Setenv("BURST_WORKER", "1")
	if !IsWorkerMode() {
		t.Fatal("expected IsWorkerMode()=true with BURST_WORKER=1")
	}
}

func TestIsWorkerMode_false(t *testing.T) {
	orig := os.Args
	defer func() { os.Args = orig }()
	os.Args = []string{"test", "--other-flag"}
	os.Unsetenv("BURST_WORKER")
	if IsWorkerMode() {
		t.Fatal("expected IsWorkerMode()=false with no worker flags")
	}
}

// --- chunkItems tests ---

func TestChunkItems_even(t *testing.T) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	chunks := chunkItems(items, 10)
	if len(chunks) != 10 {
		t.Fatalf("expected 10 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) != 10 {
			t.Fatalf("chunk %d: expected 10 items, got %d", i, len(c))
		}
	}
}

func TestChunkItems_remainder(t *testing.T) {
	items := make([]int, 101)
	for i := range items {
		items[i] = i
	}
	chunks := chunkItems(items, 10)
	if len(chunks) != 10 {
		t.Fatalf("expected 10 chunks, got %d", len(chunks))
	}
	// First 9 chunks have 10 items; last chunk has 11
	for i := 0; i < 9; i++ {
		if len(chunks[i]) != 10 {
			t.Fatalf("chunk %d: expected 10 items, got %d", i, len(chunks[i]))
		}
	}
	if len(chunks[9]) != 11 {
		t.Fatalf("last chunk: expected 11 items, got %d", len(chunks[9]))
	}
	// Verify all items are present
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if total != 101 {
		t.Fatalf("expected 101 total items, got %d", total)
	}
}

func TestChunkItems_fewerItemsThanWorkers(t *testing.T) {
	items := []int{1, 2, 3}
	chunks := chunkItems(items, 10)
	// Should produce at most len(items) chunks
	if len(chunks) > 3 {
		t.Fatalf("expected at most 3 chunks, got %d", len(chunks))
	}
}

// --- validateSerializable tests ---

func TestValidateSerializable_pass(t *testing.T) {
	if err := validateSerializable([]int{1, 2, 3}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSerializable_fail(t *testing.T) {
	// Channels are not JSON-serializable; exported field ensures Marshal sees it.
	type unserializable struct{ Ch chan int }
	items := []unserializable{{Ch: make(chan int)}}
	if err := validateSerializable(items); err == nil {
		t.Fatal("expected error for unserializable type")
	}
}

func TestValidateSerializable_empty(t *testing.T) {
	if err := validateSerializable([]int{}); err != nil {
		t.Fatalf("unexpected error for empty slice: %v", err)
	}
}

// --- BurstPartialError tests ---

func TestBurstPartialError_counts(t *testing.T) {
	e := &BurstPartialError{
		Results: []json.RawMessage{
			json.RawMessage("1"),
			nil,
			json.RawMessage("3"),
			nil,
		},
		Errors:       []string{"", "task 1 failed", "", "task 3 failed"},
		FailedCount:  2,
		SuccessCount: 2,
	}
	if e.FailedCount != 2 {
		t.Fatalf("expected FailedCount=2, got %d", e.FailedCount)
	}
	if e.SuccessCount != 2 {
		t.Fatalf("expected SuccessCount=2, got %d", e.SuccessCount)
	}
	if e.Error() == "" {
		t.Fatal("Error() should return non-empty string")
	}
	var target *BurstPartialError
	if !errors.As(e, &target) {
		t.Fatal("errors.As should find BurstPartialError")
	}
}

// --- assembleResults tests ---

func TestAssembleResults_success(t *testing.T) {
	payloads := []*resultPayload{
		{
			Results: []json.RawMessage{json.RawMessage("1"), json.RawMessage("2")},
			Errors:  []string{"", ""},
		},
		{
			Results: []json.RawMessage{json.RawMessage("3")},
			Errors:  []string{""},
		},
	}
	results, err := assembleResults[int](payloads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, expected := range []int{1, 2, 3} {
		if results[i] != expected {
			t.Fatalf("result[%d]: expected %d, got %d", i, expected, results[i])
		}
	}
}

func TestAssembleResults_partial(t *testing.T) {
	payloads := []*resultPayload{
		{
			Results: []json.RawMessage{json.RawMessage("1"), nil},
			Errors:  []string{"", "task failed"},
		},
	}
	_, err := assembleResults[int](payloads)
	if err == nil {
		t.Fatal("expected BurstPartialError")
	}
	var pe *BurstPartialError
	if !errors.As(err, &pe) {
		t.Fatalf("expected BurstPartialError, got %T", err)
	}
	if pe.FailedCount != 1 {
		t.Fatalf("expected FailedCount=1, got %d", pe.FailedCount)
	}
	if pe.SuccessCount != 1 {
		t.Fatalf("expected SuccessCount=1, got %d", pe.SuccessCount)
	}
}

// --- EnvHash tests ---

func TestEnvHash_stable(t *testing.T) {
	// Create a temp file
	f, err := os.CreateTemp("", "aji-hash-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	fmt.Fprint(f, "test content")
	f.Close()

	h1, err := EnvHash(f.Name())
	if err != nil {
		t.Fatalf("EnvHash error: %v", err)
	}
	h2, err := EnvHash(f.Name())
	if err != nil {
		t.Fatalf("EnvHash error: %v", err)
	}
	if h1 != h2 {
		t.Fatal("EnvHash is not stable for same file")
	}
	if len(h1) != 64 {
		t.Fatalf("expected 64-char hex hash, got %d chars", len(h1))
	}
}

// --- extractAccountID tests ---

func TestExtractAccountID(t *testing.T) {
	uri := "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	if got := extractAccountID(uri); got != "123456789012" {
		t.Fatalf("expected %q, got %q", "123456789012", got)
	}
}
