package firecracker

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const execProtocolVersion = 1

const frameHeaderSize = 4

// ExecProtocolStatus indicates whether command execution finished normally.
type ExecProtocolStatus string

const (
	ExecProtocolStatusOK    ExecProtocolStatus = "ok"
	ExecProtocolStatusError ExecProtocolStatus = "error"
)

// ExecProtocolErrorCode is a machine-readable protocol-level error code.
type ExecProtocolErrorCode string

const (
	ExecProtocolErrorInvalidRequest      ExecProtocolErrorCode = "invalid_request"
	ExecProtocolErrorTimeout             ExecProtocolErrorCode = "timeout"
	ExecProtocolErrorOutputLimitExceeded ExecProtocolErrorCode = "output_limit_exceeded"
	ExecProtocolErrorAgentUnavailable    ExecProtocolErrorCode = "agent_unavailable"
	ExecProtocolErrorInternal            ExecProtocolErrorCode = "internal_error"

	ExecProtocolErrorFileNotFound      ExecProtocolErrorCode = "file_not_found"
	ExecProtocolErrorPathNotAbsolute   ExecProtocolErrorCode = "path_not_absolute"
	ExecProtocolErrorFileTooLarge      ExecProtocolErrorCode = "file_too_large"
	ExecProtocolErrorEditNoMatch       ExecProtocolErrorCode = "edit_no_match"
	ExecProtocolErrorEditMultipleMatch ExecProtocolErrorCode = "edit_multiple_match"
	ExecProtocolErrorPermissionDenied  ExecProtocolErrorCode = "permission_denied"
	ExecProtocolErrorIsDirectory       ExecProtocolErrorCode = "is_directory"
	ExecProtocolErrorInvalidMethod     ExecProtocolErrorCode = "invalid_method"
)

// execRequest is sent by host provider to guest agent.
type execRequest struct {
	ProtocolVersion  int      `json:"protocol_version"`
	RequestID        string   `json:"request_id"`
	Argv             []string `json:"argv"`
	TimeoutMS        int64    `json:"timeout_ms"`
	StdoutLimitBytes int      `json:"stdout_limit_bytes"`
	StderrLimitBytes int      `json:"stderr_limit_bytes"`
}

// execResponse is sent by guest agent to host provider.
type execResponse struct {
	ProtocolVersion int                   `json:"protocol_version"`
	RequestID       string                `json:"request_id"`
	Status          ExecProtocolStatus    `json:"status"`
	ExitCode        int                   `json:"exit_code"`
	Stdout          string                `json:"stdout"`
	Stderr          string                `json:"stderr"`
	StdoutTruncated bool                  `json:"stdout_truncated"`
	StderrTruncated bool                  `json:"stderr_truncated"`
	ErrorCode       ExecProtocolErrorCode `json:"error_code,omitempty"`
	ErrorMessage    string                `json:"error_message,omitempty"`
	DurationMS      int64                 `json:"duration_ms"`
}

// RPCRequest is the unified request envelope for all guest agent RPC methods.
// When Method is empty, the agent treats it as "exec_shell" for backward compat.
type RPCRequest struct {
	ProtocolVersion int      `json:"protocol_version"`
	RequestID       string   `json:"request_id"`
	Method          string   `json:"method"`
	TimeoutMS       int64    `json:"timeout_ms"`

	// exec_shell
	Argv             []string `json:"argv,omitempty"`
	StdoutLimitBytes int      `json:"stdout_limit_bytes,omitempty"`
	StderrLimitBytes int      `json:"stderr_limit_bytes,omitempty"`

	// file operations
	Path       string `json:"path,omitempty"`
	Offset     int    `json:"offset,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Content    string `json:"content,omitempty"`
	Mode       int    `json:"mode,omitempty"`
	OldString  string `json:"old_string,omitempty"`
	NewString  string `json:"new_string,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func writeFramedJSON(w io.Writer, value any, maxPayloadBytes int) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal framed json: %w", err)
	}

	if len(payload) == 0 {
		return fmt.Errorf("marshal framed json: empty payload")
	}

	if maxPayloadBytes > 0 && len(payload) > maxPayloadBytes {
		return fmt.Errorf("marshal framed json: payload too large: %d > %d", len(payload), maxPayloadBytes)
	}

	header := make([]byte, frameHeaderSize)
	binary.BigEndian.PutUint32(header, uint32(len(payload)))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write framed json header: %w", err)
	}

	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write framed json payload: %w", err)
	}

	return nil
}

func readFramedJSON(r io.Reader, maxPayloadBytes int, out any) error {
	header := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return fmt.Errorf("read framed json header: %w", err)
	}

	size := int(binary.BigEndian.Uint32(header))
	if size <= 0 {
		return fmt.Errorf("read framed json: invalid payload size %d", size)
	}

	if maxPayloadBytes > 0 && size > maxPayloadBytes {
		return fmt.Errorf("read framed json: payload too large: %d > %d", size, maxPayloadBytes)
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return fmt.Errorf("read framed json payload: %w", err)
	}

	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("unmarshal framed json: %w", err)
	}

	return nil
}
