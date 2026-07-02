package core

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "github.com/kubescape/kubescape/v3/core/meta/datastructures/v1"
)

// minimalScanReport is the minimal v2 JSON schema that diff.Compute understands.
type minimalReport struct {
	Results        []minimalResult `json:"results"`
	SummaryDetails minimalSummary  `json:"summaryDetails"`
}

type minimalResult struct {
	ResourceID         string           `json:"resourceID"`
	AssociatedControls []minimalControl `json:"controls"`
}

type minimalControl struct {
	ControlID string            `json:"controlID"`
	Name      string            `json:"name"`
	Status    map[string]string `json:"status"`
}

type minimalSummary struct {
	Controls map[string]minimalControlSummary `json:"controls"`
}

type minimalControlSummary struct {
	ScoreFactor float32 `json:"scoreFactor"`
	Severity    string  `json:"severity,omitempty"`
}

// writeScanReport marshals r as JSON into a temp file and returns its path.
func writeScanReport(t *testing.T, r minimalReport) string {
	t.Helper()
	data, err := json.Marshal(r)
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "scan-*.json")
	require.NoError(t, err)
	_, err = f.Write(data)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// makePassingReport returns a report with a single resource that passes the given control.
func makePassingReport(controlID string) minimalReport {
	return minimalReport{
		Results: []minimalResult{
			{
				ResourceID: "resource/test",
				AssociatedControls: []minimalControl{
					{ControlID: controlID, Name: "Test Control", Status: map[string]string{"status": "passed"}},
				},
			},
		},
		SummaryDetails: minimalSummary{
			Controls: map[string]minimalControlSummary{
				controlID: {ScoreFactor: 7.0},
			},
		},
	}
}

// makeFailingReport returns a report with a single resource that fails the given control.
func makeFailingReport(controlID, severity string) minimalReport {
	return minimalReport{
		Results: []minimalResult{
			{
				ResourceID: "resource/test",
				AssociatedControls: []minimalControl{
					{ControlID: controlID, Name: "Test Control", Status: map[string]string{"status": "failed"}},
				},
			},
		},
		SummaryDetails: minimalSummary{
			Controls: map[string]minimalControlSummary{
				controlID: {Severity: severity, ScoreFactor: 7.0},
			},
		},
	}
}

// newKS returns a zero-value Kubescape ready for unit-testing.
func newKS() *Kubescape {
	return NewKubescape(context.Background())
}

func TestSeverityLabel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty threshold returns all",
			input: "",
			want:  "all",
		},
		{
			name:  "non-empty threshold is returned as-is",
			input: "high",
			want:  "high",
		},
		{
			name:  "preserves casing",
			input: "Critical",
			want:  "Critical",
		},
		{
			name:  "medium severity preserved",
			input: "medium",
			want:  "medium",
		},
		{
			name:  "arbitrary string passed through",
			input: "unknown-level",
			want:  "unknown-level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, severityLabel(tt.input))
		})
	}
}

func TestDiff_MissingBaseFile_ReturnsError(t *testing.T) {
	ks := newKS()
	info := &metav1.DiffInfo{
		BaseFile: filepath.Join(t.TempDir(), "nonexistent-base.json"),
		HeadFile: filepath.Join(t.TempDir(), "nonexistent-head.json"),
		Output:   filepath.Join(t.TempDir(), "out.txt"),
	}

	err := ks.Diff(info)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-base.json")
}

func TestDiff_PrettyOutput_NoNewFailures(t *testing.T) {
	// base and head are identical: nothing new, nothing resolved
	base := makePassingReport("C-001")
	head := makePassingReport("C-001")

	outPath := filepath.Join(t.TempDir(), "diff-output.txt")
	info := &metav1.DiffInfo{
		BaseFile: writeScanReport(t, base),
		HeadFile: writeScanReport(t, head),
		Format:   "pretty-printer", // default (non-json) branch
		Output:   outPath,
	}

	err := newKS().Diff(info)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Summary:")
}

func TestDiff_PrettyOutput_NewFailure(t *testing.T) {
	// control passes in base, fails in head → one new failure
	base := makePassingReport("C-001")
	head := makeFailingReport("C-001", "High")

	outPath := filepath.Join(t.TempDir(), "diff-output.txt")
	info := &metav1.DiffInfo{
		BaseFile: writeScanReport(t, base),
		HeadFile: writeScanReport(t, head),
		Output:   outPath,
	}

	err := newKS().Diff(info)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "New failures")
	assert.Contains(t, content, "C-001")
}

func TestDiff_PrettyOutput_ResolvedFailure(t *testing.T) {
	// control fails in base, passes in head → one resolved failure
	base := makeFailingReport("C-001", "High")
	head := makePassingReport("C-001")

	outPath := filepath.Join(t.TempDir(), "diff-output.txt")
	info := &metav1.DiffInfo{
		BaseFile: writeScanReport(t, base),
		HeadFile: writeScanReport(t, head),
		Output:   outPath,
	}

	err := newKS().Diff(info)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Resolved")
}

func TestDiff_JSONOutput_EmptyChangeSet(t *testing.T) {
	// Both reports are identical passing scans → empty diff
	base := makePassingReport("C-002")
	head := makePassingReport("C-002")

	outPath := filepath.Join(t.TempDir(), "diff.json")
	info := &metav1.DiffInfo{
		BaseFile: writeScanReport(t, base),
		HeadFile: writeScanReport(t, head),
		Format:   "json",
		Output:   outPath,
	}

	err := newKS().Diff(info)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	// Output must be valid JSON with the three expected top-level keys
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &result), "output must be valid JSON")
	assert.Contains(t, result, "new")
	assert.Contains(t, result, "resolved")
	assert.Contains(t, result, "unchanged")
}

func TestDiff_JSONOutput_NewFailure(t *testing.T) {
	// control passes in base, fails in head → new array has one entry
	base := makePassingReport("C-003")
	head := makeFailingReport("C-003", "Critical")

	outPath := filepath.Join(t.TempDir(), "diff.json")
	info := &metav1.DiffInfo{
		BaseFile: writeScanReport(t, base),
		HeadFile: writeScanReport(t, head),
		Format:   "json",
		Output:   outPath,
	}

	err := newKS().Diff(info)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	var result struct {
		New []struct {
			ControlID string `json:"controlID"`
			Severity  string `json:"severity"`
		} `json:"new"`
	}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result.New, 1)
	assert.Equal(t, "C-003", result.New[0].ControlID)
	assert.Equal(t, "Critical", result.New[0].Severity)
}

func TestDiff_FailOnNew_NoMatchesAboveThreshold_NoError(t *testing.T) {
	// Only a Low failure in head; threshold is High → filter returns empty → no fatal
	base := makePassingReport("C-004")
	head := makeFailingReport("C-004", "Low")

	outPath := filepath.Join(t.TempDir(), "out.txt")
	info := &metav1.DiffInfo{
		BaseFile:          writeScanReport(t, base),
		HeadFile:          writeScanReport(t, head),
		Output:            outPath,
		FailOnNew:         true,
		SeverityThreshold: "High",
	}

	// Should NOT call logger.Fatal because no failures are at/above High
	err := newKS().Diff(info)
	require.NoError(t, err)
}

func TestDiff_FailOnNew_Disabled_NoError(t *testing.T) {
	// Even with critical failures, FailOnNew=false must not trigger a fatal
	base := makePassingReport("C-005")
	head := makeFailingReport("C-005", "Critical")

	outPath := filepath.Join(t.TempDir(), "out.txt")
	info := &metav1.DiffInfo{
		BaseFile:  writeScanReport(t, base),
		HeadFile:  writeScanReport(t, head),
		Output:    outPath,
		FailOnNew: false,
	}

	err := newKS().Diff(info)
	require.NoError(t, err)
}

func TestDiff_StdoutOutput_DoesNotError(t *testing.T) {
	// When Output is empty, GetWriter returns os.Stdout; we just verify no error.
	base := makePassingReport("C-006")
	head := makePassingReport("C-006")

	// Redirect stdout so the test doesn't pollute terminal output.
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	defer func() {
		w.Close()
		os.Stdout = old
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
	}()

	info := &metav1.DiffInfo{
		BaseFile: writeScanReport(t, base),
		HeadFile: writeScanReport(t, head),
		Output:   "", // empty → stdout
	}

	err = newKS().Diff(info)
	require.NoError(t, err)

	_ = strings.Contains // keep import used
}
