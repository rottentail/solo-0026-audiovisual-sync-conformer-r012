package domain

import "testing"

func TestAnalysisStateTransitions(t *testing.T) {
	allowed := []struct {
		from, to string
	}{
		{AnalysisStateQueued, AnalysisStateDetecting},
		{AnalysisStateQueued, AnalysisStateFailed},
		{AnalysisStateDetecting, AnalysisStateReady},
		{AnalysisStateDetecting, AnalysisStateFailed},
	}
	for _, transition := range allowed {
		if !CanAdvanceAnalysis(transition.from, transition.to) {
			t.Fatalf("transition %s -> %s must be allowed", transition.from, transition.to)
		}
	}
	forbidden := []struct {
		from, to string
	}{
		{AnalysisStateQueued, AnalysisStateReady},
		{AnalysisStateReady, AnalysisStateFailed},
		{AnalysisStateFailed, AnalysisStateReady},
		{AnalysisStateReady, AnalysisStateDetecting},
		{AnalysisStateFailed, AnalysisStateQueued},
	}
	for _, transition := range forbidden {
		if CanAdvanceAnalysis(transition.from, transition.to) {
			t.Fatalf("transition %s -> %s must be rejected", transition.from, transition.to)
		}
	}
	if !AnalysisTerminal(AnalysisStateReady) || !AnalysisTerminal(AnalysisStateFailed) {
		t.Fatal("ready and failed must be terminal")
	}
	if AnalysisTerminal(AnalysisStateQueued) || AnalysisTerminal(AnalysisStateDetecting) {
		t.Fatal("queued and detecting must not be terminal")
	}
}

func TestConformStateTransitions(t *testing.T) {
	allowed := []struct {
		from, to string
	}{
		{ConformStateQueued, ConformStateRendering},
		{ConformStateQueued, ConformStateFailed},
		{ConformStateRendering, ConformStateCommitting},
		{ConformStateRendering, ConformStateFailed},
		{ConformStateCommitting, ConformStateCompleted},
		{ConformStateCommitting, ConformStateFailed},
	}
	for _, transition := range allowed {
		if !CanAdvanceConform(transition.from, transition.to) {
			t.Fatalf("transition %s -> %s must be allowed", transition.from, transition.to)
		}
	}
	forbidden := []struct {
		from, to string
	}{
		{ConformStateQueued, ConformStateCompleted},
		{ConformStateQueued, ConformStateCommitting},
		{ConformStateCompleted, ConformStateFailed},
		{ConformStateFailed, ConformStateCompleted},
		{ConformStateRendering, ConformStateQueued},
	}
	for _, transition := range forbidden {
		if CanAdvanceConform(transition.from, transition.to) {
			t.Fatalf("transition %s -> %s must be rejected", transition.from, transition.to)
		}
	}
	if !ConformTerminal(ConformStateCompleted) || !ConformTerminal(ConformStateFailed) {
		t.Fatal("completed and failed must be terminal")
	}
}

func TestIdentifierValidation(t *testing.T) {
	valid := []string{"abc", "take-01", "sync_analysis_2", "a12-34_xy"}
	for _, id := range valid {
		if err := ValidateIdentifier("id", id); err != nil {
			t.Fatalf("identifier %q should be valid: %v", id, err)
		}
	}
	invalid := []string{"", "ab", "1abc", "UPPER", "with space", "x/y", "-abc"}
	for _, id := range invalid {
		if err := ValidateIdentifier("id", id); err == nil {
			t.Fatalf("identifier %q should be rejected", id)
		}
	}
}

func TestRequestValidationBounds(t *testing.T) {
	if err := ValidateCreateAnalysis(CreateAnalysisInput{AnalysisID: "ana-01", MinConfidence: 0.5, MaxOffsetMS: 500}); err != nil {
		t.Fatalf("valid analysis input rejected: %v", err)
	}
	if err := ValidateCreateAnalysis(CreateAnalysisInput{AnalysisID: "ana-01", MinConfidence: 1.2, MaxOffsetMS: 500}); err == nil {
		t.Fatal("confidence above 1 must be rejected")
	}
	if err := ValidateCreateAnalysis(CreateAnalysisInput{AnalysisID: "ana-01", MinConfidence: 0.5, MaxOffsetMS: 0}); err == nil {
		t.Fatal("zero max offset must be rejected")
	}
	if err := ValidateCreateConform(CreateConformInput{ConformID: "cfm-01", MaxCorrectionMS: 1}); err != nil {
		t.Fatalf("valid conform input rejected: %v", err)
	}
	if err := ValidateCreateConform(CreateConformInput{ConformID: "cfm-01", MaxCorrectionMS: MaxCorrectionMS + 1}); err == nil {
		t.Fatal("correction above the hard maximum must be rejected")
	}
}
