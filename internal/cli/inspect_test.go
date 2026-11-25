package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/wilsonqc223/kite-x402-client/internal/x402"
)

func TestWriteInspectionShowsAShortBodyWhole(t *testing.T) {
	var out strings.Builder
	WriteInspection(&out, "http://x/", -1, x402.Inspection{Status: 200, Body: []byte(`{"free":true}`)}, "")
	got := out.String()
	if !strings.Contains(got, `{"free":true}`+"\n") || strings.Contains(got, "not shown") {
		t.Errorf("a short body is shown whole, with no note:\n%s", got)
	}
}

func TestWriteInspectionShowsTheStartOfABigBodyAndSaysHowMuchIsLeftOut(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 3*inspectPreviewBytes)
	var out strings.Builder
	WriteInspection(&out, "http://x/", -1, x402.Inspection{Status: 200, Body: body}, "")
	got := out.String()
	if strings.Contains(got, strings.Repeat("a", inspectPreviewBytes+1)) {
		t.Errorf("the report shows more than %d bytes of the body", inspectPreviewBytes)
	}
	want := fmt.Sprintf("(%d more bytes of the body not shown)", len(body)-inspectPreviewBytes)
	if !strings.Contains(got, want) {
		t.Errorf("the report must say how much of the body it left out, want %q:\n%.300s", want, got)
	}
}

func TestWriteInspectionNotesABodyCutOffAtTheReadingLimit(t *testing.T) {
	var out strings.Builder
	WriteInspection(&out, "http://x/", -1, x402.Inspection{
		Status:        200,
		Body:          bytes.Repeat([]byte("a"), inspectPreviewBytes+1),
		BodyTruncated: true,
	}, "")
	if !strings.Contains(out.String(), "and the body itself was cut off at the reading limit") {
		t.Errorf("a body cut off by the reading limit must be reported as such:\n%.300s", out.String())
	}
}
