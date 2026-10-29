package content

import (
	"strings"
	"testing"
)

func TestParseSectionsAnchorsPathsAndBodies(t *testing.T) {
	doc := "# 第1章 标题\n\n## 1.1 小节\n\n正文内容。\n\n### 1.1.1 子节\n\n更多。\n"
	sections := ParseSections(doc)
	if len(sections) != 3 {
		t.Fatalf("sections = %d, want 3", len(sections))
	}
	if sections[0].Anchor != "section-0" {
		t.Errorf("first anchor = %q, want section-0", sections[0].Anchor)
	}
	if sections[0].Level != 1 || len(sections[0].Path) != 0 {
		t.Errorf("first section level/path = %d/%v", sections[0].Level, sections[0].Path)
	}
	if sections[1].Anchor != "section-10" {
		t.Errorf("second anchor = %q, want section-10", sections[1].Anchor)
	}
	if got := sections[1].Path; len(got) != 1 || got[0] != "section-0" {
		t.Errorf("second path = %v, want [section-0]", got)
	}
	if !strings.Contains(sections[1].Body, "正文内容") {
		t.Errorf("second body = %q, want the paragraph", sections[1].Body)
	}
	if got := sections[2].Path; len(got) != 2 || got[0] != "section-0" || got[1] != sections[1].Anchor {
		t.Errorf("third path = %v", got)
	}
	// The second section's body stops at the third heading, so it cannot claim
	// the child section text.
	if strings.Contains(sections[1].Body, "更多") {
		t.Errorf("second body leaked child section text: %q", sections[1].Body)
	}
}

func TestParseSectionsSkipsLevelFive(t *testing.T) {
	doc := "# 标题\n\n##### 1. 细节\n\n细节正文。\n\n## 小结\n\n结束。\n"
	sections := ParseSections(doc)
	if len(sections) != 2 {
		t.Fatalf("sections = %+v, want 2 headings (level 5 is body text)", sections)
	}
	if !strings.Contains(sections[0].Body, "细节正文") {
		t.Errorf("level 5 content should stay in the parent body: %q", sections[0].Body)
	}
}

func TestStableKeySurvivesOffsetShift(t *testing.T) {
	base := "# 第6章 数据库设计\n\n## 6.1 数据库基本概念\n\n内容。\n\n## 6.2 关系数据库\n\n内容。\n"
	shifted := "# 第6章 数据库设计\n\n新增的一段说明文字。\n\n## 6.1 数据库基本概念\n\n内容。\n\n## 6.2 关系数据库\n\n内容。\n"
	first := ParseSections(base)
	second := ParseSections(shifted)
	if len(first) != len(second) {
		t.Fatalf("section counts differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Anchor == second[i].Anchor && i > 0 {
			t.Errorf("anchor %d did not shift: %q", i, first[i].Anchor)
		}
		if first[i].StableKey != second[i].StableKey {
			t.Errorf("stable key %d changed: %q vs %q (title %q)", i, first[i].StableKey, second[i].StableKey, first[i].Title)
		}
	}
}

func TestStableKeyDistinguishesDuplicateTitles(t *testing.T) {
	doc := "# 前言\n\n## 内容简介\n\n正文。\n\n## 前言\n\n正文。\n"
	sections := ParseSections(doc)
	if len(sections) != 3 {
		t.Fatalf("sections = %d", len(sections))
	}
	if sections[1].StableKey == sections[2].StableKey {
		t.Errorf("duplicate sibling titles share a stable key: %q", sections[1].StableKey)
	}
}

func TestUTF16OffsetsMatchBrowserSlicing(t *testing.T) {
	// The emoji is one rune but two UTF-16 code units; the browser reader slices
	// with JavaScript string offsets, so anchors must use UTF-16 arithmetic.
	doc := "# 😀标题\n\n## 小节\n\n正文。\n"
	sections := ParseSections(doc)
	if len(sections) != 2 {
		t.Fatalf("sections = %d", len(sections))
	}
	if utf16Len("# 😀标题") != 6 {
		t.Fatalf("utf16Len = %d, want 6", utf16Len("# 😀标题"))
	}
	if sections[1].Anchor != "section-8" {
		t.Errorf("second anchor = %q, want section-8 (UTF-16 offset)", sections[1].Anchor)
	}
}

func TestParseSectionsCleansInlineMarkdownTitles(t *testing.T) {
	doc := "# 标题\n\n## 第三段：实践过程（1000-1200 字，**核心**）\n\n正文。\n"
	sections := ParseSections(doc)
	if sections[1].Title != "第三段：实践过程（1000-1200 字，核心）" {
		t.Errorf("title = %q", sections[1].Title)
	}
}

func TestParseSectionsEmptyDocument(t *testing.T) {
	if sections := ParseSections(""); len(sections) != 0 {
		t.Errorf("empty document produced %d sections", len(sections))
	}
	sections := ParseSections("只有正文，没有标题。")
	if len(sections) != 1 || sections[0].Anchor != "section-0" {
		t.Errorf("headingless document = %+v, want one synthetic section", sections)
	}
}
