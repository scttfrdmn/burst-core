package aji

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// taskPayload is the JSON-encoded task file written to S3.
type taskPayload struct {
	Items        json.RawMessage `json:"items"`
	FunctionName string          `json:"function"`
	ChunkIndex   int             `json:"chunk_index"`
}

// resultPayload is the JSON-encoded result file written to S3 by the worker.
type resultPayload struct {
	Results []json.RawMessage `json:"results"`
	Errors  []string          `json:"errors"` // "" where task succeeded
}

// SerializeTask encodes the function name, chunk index, and items into a task payload.
// items must be a slice of JSON-serializable values.
func SerializeTask(fnName string, chunkIdx int, items any) ([]byte, error) {
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("serializing task items: %w", err)
	}
	p := taskPayload{
		Items:        json.RawMessage(itemsJSON),
		FunctionName: fnName,
		ChunkIndex:   chunkIdx,
	}
	return json.Marshal(p)
}

// DeserializeTask parses the task payload from raw bytes.
func DeserializeTask(data []byte) (*taskPayload, error) {
	var p taskPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("deserializing task: %w", err)
	}
	return &p, nil
}

// DeserializeItems unmarshals the raw JSON items array into []any, where each
// element is properly typed as t (the function's input type).
func DeserializeItems(raw json.RawMessage, t reflect.Type) ([]any, error) {
	sliceType := reflect.SliceOf(t)
	slicePtr := reflect.New(sliceType)
	if err := json.Unmarshal(raw, slicePtr.Interface()); err != nil {
		return nil, fmt.Errorf("deserializing items as []%s: %w", t, err)
	}
	slice := slicePtr.Elem()
	items := make([]any, slice.Len())
	for i := range items {
		items[i] = slice.Index(i).Interface()
	}
	return items, nil
}

// SerializeResult encodes results and per-item error strings into a result payload.
// results[i] is nil and errs[i] is non-empty where item i failed.
func SerializeResult(results []any, errs []string) ([]byte, error) {
	rawResults := make([]json.RawMessage, len(results))
	for i, r := range results {
		if r == nil {
			rawResults[i] = json.RawMessage("null")
			continue
		}
		b, err := json.Marshal(r)
		if err != nil {
			return nil, fmt.Errorf("serializing result[%d]: %w", i, err)
		}
		rawResults[i] = b
	}
	return json.Marshal(resultPayload{Results: rawResults, Errors: errs})
}

// DeserializeResult parses the result payload from raw bytes.
func DeserializeResult(data []byte) (*resultPayload, error) {
	var p resultPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("deserializing result: %w", err)
	}
	return &p, nil
}

// validateSerializable smoke-tests the first item in the slice for JSON marshaling.
// Returns a clear error before any AWS resources are provisioned.
func validateSerializable[T any](items []T) error {
	if len(items) == 0 {
		return nil
	}
	if _, err := json.Marshal(items[0]); err != nil {
		return fmt.Errorf("aji: items must be JSON-serializable; first item marshal failed: %w", err)
	}
	return nil
}

