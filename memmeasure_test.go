package main

import (
	"strings"
	"testing"
)

func TestStepsInMainPrompt(t *testing.T) {
	for _, mem := range []bool{true, false} {
		s := stepsInMainPrompt(systemPrompt("/Users/me/AGENTS.md", "", false, mem))
		if !strings.Contains(s, stepsInMainZoom) || !strings.Contains(s, workKindLine+stepsInMainKind) {
			t.Fatalf("agent memory %v: the prompt does not say steps are in the chat", mem)
		}
		if strings.Contains(s, zoomAgentLines) || strings.Contains(s, zoomAgentWhole) {
			t.Fatalf("agent memory %v: the prompt still offers zoom(\"Name\")", mem)
		}
	}
}
