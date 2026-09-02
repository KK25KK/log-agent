package domain

const (
	RuntimeAnchorVersion       = "runtime-anchor-v1"
	RuntimeAnchorPerEventLimit = 4
	RuntimeAnchorGlobalLimit   = 64
)

type RuntimeAnchorKind string

const (
	RuntimeAnchorErrorText  RuntimeAnchorKind = "ERROR_TEXT"
	RuntimeAnchorErrorType  RuntimeAnchorKind = "ERROR_TYPE"
	RuntimeAnchorRoute      RuntimeAnchorKind = "ROUTE"
	RuntimeAnchorSymbol     RuntimeAnchorKind = "SYMBOL"
	RuntimeAnchorStackFrame RuntimeAnchorKind = "STACK_FRAME"
)

type RuntimeAnchor struct {
	ID          string            `json:"id"`
	Kind        RuntimeAnchorKind `json:"kind"`
	EventID     string            `json:"event_id"`
	MemberID    string            `json:"member_id"`
	Value       string            `json:"value,omitempty"`
	File        string            `json:"file,omitempty"`
	Line        int               `json:"line,omitempty"`
	Symbol      string            `json:"symbol,omitempty"`
	Fingerprint string            `json:"fingerprint"`
}

type RuntimeAnchorSetStatus string

const (
	RuntimeAnchorsComplete RuntimeAnchorSetStatus = "COMPLETE"
	RuntimeAnchorsPartial  RuntimeAnchorSetStatus = "PARTIAL"
	RuntimeAnchorsNone     RuntimeAnchorSetStatus = "NO_ANCHORS"
)

type RuntimeAnchorSet struct {
	Version            string                 `json:"version"`
	Status             RuntimeAnchorSetStatus `json:"status"`
	SourceEventCount   int                    `json:"source_event_count"`
	AnchoredEventCount int                    `json:"anchored_event_count"`
	DroppedCount       int                    `json:"dropped_count"`
	Limit              int                    `json:"limit"`
	Anchors            []RuntimeAnchor        `json:"anchors"`
}
