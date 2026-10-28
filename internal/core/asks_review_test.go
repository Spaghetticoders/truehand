package core

import "testing"

// Agents write markdown: a question formatted the usual ways must still reach the human.
func TestReviewAsksSeesFormattedQuestions(t *testing.T) {
	for _, q := range []string{
		"**Domanda:** scegli tra A e B.",
		"- Domanda: usiamo Riverpod o Bloc.",
		"> Question: which base branch.",
		"Quale preferisci?)",
		"Ok per te? 🙏",
		"Procedo con A o B? **",
	} {
		if !asks(q) {
			t.Errorf("missed: %q", q)
		}
	}
	if asks("Deciso: usiamo A.\nFatto.") {
		t.Error("a plain decision log counts as a question")
	}
}
