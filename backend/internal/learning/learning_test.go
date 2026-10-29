package learning_test

import (
	"strings"
	"testing"
	"time"

	"rka/internal/learning"
)

func TestEvidenceKindOf(t *testing.T) {
	cases := []struct {
		name     string
		correct  bool
		assisted bool
		want     learning.EvidenceKind
	}{
		{"wrong answer is wrong evidence", false, false, learning.KindWrong},
		{"wrong with hint stays wrong", false, true, learning.KindWrong},
		{"correct with hint is assisted", true, true, learning.KindAssistedPass},
		{"correct alone is independent", true, false, learning.KindIndependentPass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := learning.EvidenceKindOf(tc.correct, tc.assisted); got != tc.want {
				t.Fatalf("EvidenceKindOf(%v, %v) = %q, want %q", tc.correct, tc.assisted, got, tc.want)
			}
		})
	}
}

func TestReviewIntervalDays(t *testing.T) {
	cases := []struct {
		name     string
		correct  bool
		assisted bool
		want     int
	}{
		{"wrong comes back tomorrow", false, false, 1},
		{"assisted comes back in two days", true, true, 2},
		{"independent comes back in a week", true, false, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := learning.ReviewIntervalDays(tc.correct, tc.assisted); got != tc.want {
				t.Fatalf("ReviewIntervalDays(%v, %v) = %d, want %d", tc.correct, tc.assisted, got, tc.want)
			}
		})
	}
}

func TestReviewDueAtAddsIntervalToAnswerTime(t *testing.T) {
	answered := time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		correct  bool
		assisted bool
		want     time.Time
	}{
		{false, false, answered.Add(24 * time.Hour)},
		{true, true, answered.Add(48 * time.Hour)},
		{true, false, answered.Add(7 * 24 * time.Hour)},
	}
	for _, tc := range cases {
		got := learning.ReviewDueAt(answered, tc.correct, tc.assisted)
		if !got.Equal(tc.want) {
			t.Fatalf("ReviewDueAt(%v, %v) = %s, want %s", tc.correct, tc.assisted, got, tc.want)
		}
	}
}

func TestStateFromEvidence(t *testing.T) {
	cases := []struct {
		name  string
		kinds []learning.EvidenceKind
		want  learning.MasteryState
	}{
		{"no evidence is unknown", nil, learning.MasteryUnknown},
		{"wrong answers alone do not prove mastery", []learning.EvidenceKind{learning.KindWrong}, learning.MasteryUnknown},
		{"reading is read", []learning.EvidenceKind{learning.KindReading}, learning.MasteryRead},
		{"assisted passes beat reading", []learning.EvidenceKind{learning.KindReading, learning.KindAssistedPass}, learning.MasteryAssisted},
		{"independent passes beat assisted", []learning.EvidenceKind{learning.KindAssistedPass, learning.KindIndependentPass}, learning.MasteryIndependent},
		{"self declaration is its own top state", []learning.EvidenceKind{learning.KindIndependentPass, learning.KindSelfDeclared}, learning.MasterySelfDeclared},
		{"order does not matter", []learning.EvidenceKind{learning.KindSelfDeclared, learning.KindReading}, learning.MasterySelfDeclared},
		{"hint usage alone carries no mastery", []learning.EvidenceKind{learning.KindHintUsed}, learning.MasteryUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := learning.StateFromEvidence(tc.kinds); got != tc.want {
				t.Fatalf("StateFromEvidence(%v) = %q, want %q", tc.kinds, got, tc.want)
			}
		})
	}
}

func TestMasteryRankOrdersStates(t *testing.T) {
	order := []learning.MasteryState{
		learning.MasteryUnknown,
		learning.MasteryRead,
		learning.MasteryAssisted,
		learning.MasteryIndependent,
		learning.MasterySelfDeclared,
	}
	for i := 1; i < len(order); i++ {
		if learning.MasteryRank(order[i-1]) >= learning.MasteryRank(order[i]) {
			t.Fatalf("rank(%q) should be lower than rank(%q)", order[i-1], order[i])
		}
	}
}

func TestBuildReportExplainsWrongAnswersFirst(t *testing.T) {
	conclusion := learning.BuildReport(3, 1, 2, 1)
	if conclusion.Focus != "错题复盘" {
		t.Fatalf("focus = %q, want 错题复盘", conclusion.Focus)
	}
	if len(conclusion.Evidence) != 4 {
		t.Fatalf("evidence statements = %d, want 4: %v", len(conclusion.Evidence), conclusion.Evidence)
	}
	if len(conclusion.SuggestedSteps) == 0 {
		t.Fatal("suggested steps must always be present")
	}
	joined := strings.Join(conclusion.Evidence, "|")
	for _, want := range []string{"错误", "提示辅助", "复习时间", "独立通过"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evidence statements %q should mention %q", joined, want)
		}
	}
}

func TestBuildReportUsesDueReviewWhenNoWrongAnswers(t *testing.T) {
	conclusion := learning.BuildReport(2, 0, 0, 3)
	if conclusion.Focus != "到期复习" {
		t.Fatalf("focus = %q, want 到期复习", conclusion.Focus)
	}
	if len(conclusion.Evidence) != 2 {
		t.Fatalf("evidence statements = %d, want 2: %v", len(conclusion.Evidence), conclusion.Evidence)
	}
}

func TestBuildReportIsConservativeWithoutData(t *testing.T) {
	conclusion := learning.BuildReport(0, 0, 0, 0)
	if conclusion.Focus != "继续阅读与练习" {
		t.Fatalf("focus = %q, want 继续阅读与练习", conclusion.Focus)
	}
	if !strings.Contains(conclusion.Reason, "证据不足") {
		t.Fatalf("reason should explain missing evidence, got %q", conclusion.Reason)
	}
	if len(conclusion.SuggestedSteps) == 0 {
		t.Fatal("conservative report must still suggest concrete next steps")
	}
}

func TestBuildReportNeverInventsPassRate(t *testing.T) {
	cases := [][4]int{
		{0, 0, 0, 0},
		{10, 4, 1, 2},
		{1, 0, 0, 0},
	}
	for _, counts := range cases {
		conclusion := learning.BuildReport(counts[0], counts[1], counts[2], counts[3])
		text := strings.Join(append([]string{conclusion.Focus, conclusion.Reason}, append(conclusion.Evidence, conclusion.SuggestedSteps...)...), " ")
		if strings.Contains(text, "通过率") || strings.Contains(text, "%") || strings.Contains(strings.ToLower(text), "probability") {
			t.Fatalf("report text must not claim a pass rate: %q", text)
		}
	}
}
