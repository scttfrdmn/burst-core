package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Language prefix constants for session IDs.
const (
	LangPython     = "py"
	LangJulia      = "jl"
	LangTypeScript = "ts"
	LangGo         = "go"
	LangR          = "r_"
)

// SessionKeys holds S3 key path helpers for a single burst session.
type SessionKeys struct {
	Bucket    string
	SessionID string
	Manifest  string // sessions/{id}/manifest.json
	TasksDir  string // sessions/{id}/tasks/
}

// TaskKey returns the S3 key for task input data.
func (k SessionKeys) TaskKey(taskID string) string {
	return fmt.Sprintf("sessions/%s/tasks/%s.task", k.SessionID, taskID)
}

// ResultKey returns the S3 key for task output data.
func (k SessionKeys) ResultKey(taskID string) string {
	return fmt.Sprintf("sessions/%s/tasks/%s.result", k.SessionID, taskID)
}

// StatusKey returns the S3 key for task status (pending|running|done|failed).
func (k SessionKeys) StatusKey(taskID string) string {
	return fmt.Sprintf("sessions/%s/tasks/%s.status", k.SessionID, taskID)
}

// ErrorKey returns the S3 key for task error text (only written on failure).
func (k SessionKeys) ErrorKey(taskID string) string {
	return fmt.Sprintf("sessions/%s/tasks/%s.error", k.SessionID, taskID)
}

// S3Keys returns the key helpers for a given session.
func S3Keys(bucket, sessionID string) SessionKeys {
	return SessionKeys{
		Bucket:    bucket,
		SessionID: sessionID,
		Manifest:  fmt.Sprintf("sessions/%s/manifest.json", sessionID),
		TasksDir:  fmt.Sprintf("sessions/%s/tasks/", sessionID),
	}
}

// GenerateSessionID returns a new unique session ID in the format:
//
//	{lang}-{yyyymmdd}-{random-8hex}
//
// lang must be one of the Lang* constants (py, jl, ts, go, r_).
func GenerateSessionID(lang string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is extremely rare; panic is appropriate
		panic(fmt.Sprintf("burst: crypto/rand failed: %v", err))
	}
	date := time.Now().UTC().Format("20060102")
	return fmt.Sprintf("%s-%s-%s", lang, date, hex.EncodeToString(b))
}

// TaskID returns a zero-padded task ID string for the nth task.
//
//	TaskID(0)    == "task-0000"
//	TaskID(9999) == "task-9999"
func TaskID(n int) string {
	return fmt.Sprintf("task-%04d", n)
}
