package plans

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type DiagnosticKind string

const (
	LegacyForm        DiagnosticKind = "legacy-form"
	InvalidReference  DiagnosticKind = "invalid-reference"
	KeywordMismatch   DiagnosticKind = "keyword-drift"
	PropertyMismatch  DiagnosticKind = "property-drift"
	StructureMismatch DiagnosticKind = "structure-drift"
	DanglingLink      DiagnosticKind = "dangling-link"
	UnreadablePlan    DiagnosticKind = "unreadable-plan"
)

type Diagnostic struct {
	Kind     DiagnosticKind `json:"kind"`
	Severity string         `json:"severity"`
	Plan     string         `json:"plan"`
	File     string         `json:"file,omitempty"`
	Item     string         `json:"item,omitempty"`
	Event    string         `json:"event,omitempty"`
	Property string         `json:"property,omitempty"`
	Message  string         `json:"message"`
}

type Audit struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Audit reads all hosts' plans without syncing, repairing or resealing them.
// A plan that cannot be read becomes a diagnostic; only enumeration errors
// fail the scan. Supported legacy forms are informational.
func (c Config) Audit() (Audit, error) {
	result := Audit{Diagnostics: []Diagnostic{}}
	plans, failed, err := c.Plans()
	if err != nil {
		return result, err
	}
	for _, name := range failed {
		file := filepath.Join(c.Dir, name+".org")
		p, err := c.Open(file)
		if err == nil {
			plans = append(plans, p)
		} else {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Kind: UnreadablePlan, Severity: "error", Plan: file, Message: err.Error()})
		}
	}
	for _, p := range plans {
		diagnostics, err := p.audit()
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Kind: UnreadablePlan, Severity: "error", Plan: p.File(), Message: err.Error()})
		}
	}
	key := func(d Diagnostic) string {
		return strings.Join([]string{d.Plan, string(d.Kind), d.File, d.Item, d.Event, d.Property, d.Message}, "\x00")
	}
	slices.SortFunc(result.Diagnostics, func(a, b Diagnostic) int { return strings.Compare(key(a), key(b)) })
	return result, nil
}

func (p Plan) audit() ([]Diagnostic, error) {
	var out []Diagnostic
	add := func(d Diagnostic) {
		d.Plan = p.File()
		d.Severity = "warning"
		if d.Kind == LegacyForm {
			d.Severity = "info"
		}
		out = append(out, d)
	}
	events, err := p.Events()
	if err != nil {
		return out, err
	}
	valid := make([]Event, 0, len(events))
	for _, e := range events {
		if e.Kind == "referenced" && !validReference(e.Summary) {
			add(Diagnostic{Kind: InvalidReference, File: p.log(), Event: e.ID, Item: e.Item, Message: "referenced summary is not an http or https URL"})
			continue
		}
		valid = append(valid, e)
	}
	s, err := p.Status()
	if err != nil {
		return out, err
	}
	// Invalid historical summaries are diagnosed above, rather than used
	// as URLs; valid references still participate in drift detection.
	s.PropertyDrift, err = p.propertyDrift(valid)
	if err != nil {
		return out, err
	}
	for _, d := range s.KeywordDrift {
		add(Diagnostic{Kind: KeywordMismatch, Item: d.ID, Message: fmt.Sprintf("file %q; log %q", d.File, d.Log)})
	}
	for _, d := range s.PropertyDrift {
		add(Diagnostic{Kind: PropertyMismatch, Item: d.ID, Property: d.Property, Message: fmt.Sprintf("file %q; log %q", d.File, d.Log)})
	}
	for _, d := range s.StructureDrift {
		kind := StructureMismatch
		if d.Problem == oldFormProblem {
			kind = LegacyForm
		}
		add(Diagnostic{Kind: kind, File: d.File, Message: d.Heading + ": " + d.Problem})
	}
	for _, link := range s.DanglingLinks {
		add(Diagnostic{Kind: DanglingLink, Message: link + " names no file in the plans repository"})
	}
	return out, nil
}
