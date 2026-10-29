// Package learning holds the shared learning-fact vocabulary and the pure rules
// that derive review and mastery state from recorded evidence.
//
// Rules live here (not in handlers) so they can be tested with reproducible
// inputs and versioned independently from storage.
package learning

import "time"

// RuleVersion is stored next to derived state so a rule change never silently
// rewrites history.
const RuleVersion = "v1"

type EvidenceKind string

const (
	KindReading         EvidenceKind = "reading"
	KindHintUsed        EvidenceKind = "hint_used"
	KindAssistedPass    EvidenceKind = "assisted_pass"
	KindIndependentPass EvidenceKind = "independent_pass"
	KindWrong           EvidenceKind = "wrong"
	KindSelfDeclared    EvidenceKind = "self_declared"
)

// AttemptFact is one immutable answer event.
type AttemptFact struct {
	AttemptID  string    `json:"attemptId"`
	UserID     string    `json:"userId"`
	SessionID  string    `json:"sessionId"`
	QuestionID string    `json:"questionId"`
	Revision   string    `json:"revision"`
	Selected   string    `json:"selected"`
	Correct    bool      `json:"correct"`
	Assisted   bool      `json:"assisted"`
	DurationMS int       `json:"durationMs"`
	AnsweredAt time.Time `json:"answeredAt"`
}

// ReadingFact is a reading-progress observation.
type ReadingFact struct {
	UserID     string  `json:"userId"`
	MaterialID string  `json:"materialId"`
	Progress   float64 `json:"progress"`
	SectionKey string  `json:"sectionKey"`
	Done       bool    `json:"done"`
}

// EvidenceKindOf classifies one attempt into the evidence it produces.
func EvidenceKindOf(correct, assisted bool) EvidenceKind {
	switch {
	case !correct:
		return KindWrong
	case assisted:
		return KindAssistedPass
	default:
		return KindIndependentPass
	}
}

// ReviewIntervalDays returns the review interval for one attempt.
// Wrong answers come back tomorrow, assisted passes in two days, independent
// passes in a week. This is a reminder rule, not a calibrated memory model.
func ReviewIntervalDays(correct, assisted bool) int {
	switch {
	case !correct:
		return 1
	case assisted:
		return 2
	default:
		return 7
	}
}

// ReviewDueAt returns when the question should reappear.
func ReviewDueAt(answeredAt time.Time, correct, assisted bool) time.Time {
	return answeredAt.Add(time.Duration(ReviewIntervalDays(correct, assisted)) * 24 * time.Hour)
}

// MasteryState is the highest evidence level reached for a knowledge point.
type MasteryState string

const (
	MasteryUnknown      MasteryState = "unknown"
	MasteryRead         MasteryState = "read"
	MasteryAssisted     MasteryState = "assisted"
	MasteryIndependent  MasteryState = "independent"
	MasterySelfDeclared MasteryState = "self_declared"
)

// MasteryRank orders states from weakest to strongest.
func MasteryRank(state MasteryState) int {
	switch state {
	case MasteryRead:
		return 1
	case MasteryAssisted:
		return 2
	case MasteryIndependent:
		return 3
	case MasterySelfDeclared:
		return 4
	default:
		return 0
	}
}

// StateFromEvidence derives the strongest state present in a set of facts.
// Self-declaration ranks above independent evidence only because the learner
// asserted it; it is stored as a separate state and never reported as a pass
// rate.
func StateFromEvidence(kinds []EvidenceKind) MasteryState {
	best := MasteryUnknown
	for _, kind := range kinds {
		var state MasteryState
		switch kind {
		case KindReading:
			state = MasteryRead
		case KindAssistedPass:
			state = MasteryAssisted
		case KindIndependentPass:
			state = MasteryIndependent
		case KindSelfDeclared:
			state = MasterySelfDeclared
		default:
			continue
		}
		if MasteryRank(state) > MasteryRank(best) {
			best = state
		}
	}
	return best
}

// ReportConclusion turns counts into an explainable recommendation. Every
// sentence names the evidence behind it; no pass probability is invented.
type ReportConclusion struct {
	Focus          string   `json:"focus"`
	Reason         string   `json:"reason"`
	Evidence       []string `json:"evidence"`
	SuggestedSteps []string `json:"suggestedSteps"`
}

func BuildReport(independent, assisted, wrong, due int) ReportConclusion {
	conclusion := ReportConclusion{Evidence: []string{}}
	if wrong > 0 {
		conclusion.Focus = "错题复盘"
		conclusion.Reason = "最近有独立作答仍然错误的题目。"
		conclusion.Evidence = append(conclusion.Evidence, "存在最近一次作答为错误的题目")
		conclusion.SuggestedSteps = []string{"回到原文对应小节重读", "完成一次不使用提示的复测"}
	}
	if assisted > 0 {
		conclusion.Evidence = append(conclusion.Evidence, "部分正确来自提示辅助，不等于独立掌握")
	}
	if due > 0 {
		conclusion.Evidence = append(conclusion.Evidence, "有题目到达复习时间")
		if conclusion.Focus == "" {
			conclusion.Focus = "到期复习"
			conclusion.Reason = "复习间隔已到，及时复测可以巩固记忆。"
		}
	}
	if independent > 0 {
		conclusion.Evidence = append(conclusion.Evidence, "存在独立通过记录")
	}
	if conclusion.Focus == "" {
		conclusion.Focus = "继续阅读与练习"
		conclusion.Reason = "当前证据不足，先积累一次完整的阅读与独立作答。"
		conclusion.SuggestedSteps = []string{"选择一个章节完成阅读", "完成本章关联练习"}
	}
	if len(conclusion.SuggestedSteps) == 0 {
		conclusion.SuggestedSteps = []string{"按到期顺序复习", "完成一次新的独立复测"}
	}
	return conclusion
}
