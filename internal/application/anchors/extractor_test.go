package anchors

import (
	"strings"
	"testing"

	"logagent/internal/domain"
)

func TestExtractBuildsBoundedSearchAnchorsFromRedactedGoEvent(t *testing.T) {
	events, set := Extract([]domain.TraceEvent{{
		ID: "event-1", MemberID: "dam-server", Operation: "POST /dam/job",
		Message:  "request failed: decode asset metadata\nexample.com/dam/internal/service.(*Handler).Upload\n\tC:\\repo\\internal\\service\\upload.go:42 +0x1",
		Redacted: true,
	}}, true)
	if set.Status != domain.RuntimeAnchorsComplete || len(events) != 1 || len(events[0].Anchors) != 4 || len(set.Anchors) != 4 {
		t.Fatalf("unexpected anchor set: events=%#v set=%#v", events, set)
	}
	wantKinds := map[domain.RuntimeAnchorKind]bool{
		domain.RuntimeAnchorStackFrame: false, domain.RuntimeAnchorSymbol: false,
		domain.RuntimeAnchorErrorText: false, domain.RuntimeAnchorRoute: false,
	}
	for _, anchor := range set.Anchors {
		wantKinds[anchor.Kind] = true
		if err := Validate(anchor); err != nil {
			t.Fatalf("extracted invalid anchor %#v: %v", anchor, err)
		}
		if strings.Contains(anchor.Value, "[REDACTED]") || strings.Contains(anchor.Value, "[TRACE_ID]") {
			t.Fatalf("placeholder became a search key: %#v", anchor)
		}
		if anchor.Kind == domain.RuntimeAnchorStackFrame && (anchor.File != "internal/service/upload.go" || anchor.Line != 42) {
			t.Fatalf("stack frame was not normalized: %#v", anchor)
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("missing %s anchor: %#v", kind, set.Anchors)
		}
	}
}

func TestExtractRejectsRedactionPlaceholdersAndMarksBoundedDrops(t *testing.T) {
	_, empty := Extract([]domain.TraceEvent{{ID: "event-empty", MemberID: "dam", Message: "failed: [REDACTED] for [TRACE_ID]"}}, true)
	if empty.Status != domain.RuntimeAnchorsNone || len(empty.Anchors) != 0 {
		t.Fatalf("redacted placeholder became an anchor: %#v", empty)
	}
	message := "failed: stable failure phrase AError BError CError DError EError FError"
	events, limited := Extract([]domain.TraceEvent{{ID: "event-many", MemberID: "dam", Operation: "GET /dam/jobs", Message: message}}, true)
	if limited.Status != domain.RuntimeAnchorsPartial || limited.DroppedCount == 0 || len(events[0].Anchors) != domain.RuntimeAnchorPerEventLimit {
		t.Fatalf("anchor limits were not visible: events=%#v set=%#v", events, limited)
	}
}

func TestValidateRejectsTamperedAnchor(t *testing.T) {
	events, _ := Extract([]domain.TraceEvent{{ID: "event", MemberID: "dam", Operation: "GET /dam/job", Message: "ok"}}, true)
	anchor := events[0].Anchors[0]
	anchor.Value = "DELETE /unsafe"
	if err := Validate(anchor); err == nil {
		t.Fatal("tampered runtime anchor was accepted")
	}
}
