package content

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Section is one heading parsed from a material markdown document.
//
// Anchor is version scoped: it embeds the heading offset inside that exact
// version ("section-28226"). StableKey is the cross-version identity derived
// from the heading path, so a link to "6.2 关系数据库" survives a re-import even
// when the byte offset changes.
type Section struct {
	Anchor    string
	StableKey string
	Title     string
	Level     int
	// Start is a UTF-16 code unit offset, matching JavaScript string slicing
	// used by the reader. It is only valid inside the material version that
	// produced it.
	Start int
	// ByteStart is the offset used to slice the Go string (markdown is UTF-8).
	ByteStart int
	// Path lists ancestor anchors from the document root to the direct parent.
	Path []string
	// Body is the raw markdown between this heading and the next section.
	Body string
}

var headingPattern = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t]*$`)

// ParseSections extracts the level 1–4 headings of a markdown document. Levels
// 5–6 are treated as body text of their parent section, matching the content
// snapshot's section index.
func ParseSections(markdown string) []Section {
	type heading struct {
		lineStartByte int
		lineStartU16  int
		title         string
		level         int
		bodyStartByte int
	}

	var headings []heading
	bytePos := 0
	u16Pos := 0
	lines := strings.Split(markdown, "\n")
	for _, line := range lines {
		lineBytes := len(line)
		lineU16 := utf16Len(line)
		trimmed := strings.TrimLeft(line, " \t")
		if match := headingPattern.FindStringSubmatch(trimmed); match != nil {
			level := len(match[1])
			if level <= 4 {
				headings = append(headings, heading{
					lineStartByte: bytePos,
					lineStartU16:  u16Pos + (len(line) - len(trimmed)), // byte==u16 for ASCII indentation
					title:         cleanInlineMarkdown(match[2]),
					level:         level,
					bodyStartByte: bytePos + lineBytes + 1,
				})
			}
		}
		bytePos += lineBytes + 1
		u16Pos += lineU16 + 1
	}
	if len(headings) == 0 {
		if strings.TrimSpace(markdown) == "" {
			return nil
		}
		return []Section{{
			Anchor:    "section-0",
			StableKey: stableKeyFor([]string{""}),
			Title:     "",
			Level:     1,
			Start:     0,
			ByteStart: 0,
			Body:      markdown,
		}}
	}

	sections := make([]Section, 0, len(headings))
	type ancestor struct {
		level  int
		anchor string
		title  string
	}
	var stack []ancestor
	keyCounts := map[string]int{}
	for i, h := range headings {
		for len(stack) > 0 && stack[len(stack)-1].level >= h.level {
			stack = stack[:len(stack)-1]
		}
		// Skip leading empty headings such as a blank "# " line.
		if h.title == "" && h.level == 1 && i == 0 {
			// still keep it: the snapshot keeps the document title section
		}
		path := make([]string, 0, len(stack))
		pathTitles := make([]string, 0, len(stack)+1)
		for _, a := range stack {
			path = append(path, a.anchor)
			pathTitles = append(pathTitles, a.title)
		}
		pathTitles = append(pathTitles, h.title)
		base := stableKeyFor(pathTitles)
		keyCounts[base]++
		stable := base
		if n := keyCounts[base]; n > 1 {
			stable = base + "-" + itoa(n)
		}
		end := len(markdown)
		if i+1 < len(headings) {
			end = headings[i+1].lineStartByte
		}
		body := ""
		if h.bodyStartByte <= end && end <= len(markdown) {
			body = strings.TrimSpace(markdown[h.bodyStartByte:end])
		}
		sections = append(sections, Section{
			Anchor:    "section-" + itoa(h.lineStartU16),
			StableKey: stable,
			Title:     h.title,
			Level:     h.level,
			Start:     h.lineStartU16,
			ByteStart: h.lineStartByte,
			Path:      path,
			Body:      body,
		})
		stack = append(stack, ancestor{level: h.level, anchor: "section-" + itoa(h.lineStartU16), title: h.title})
	}
	return sections
}

// stableKeyFor hashes a heading path ("6.2 关系数据库" below "第6章 ...") so the
// same conceptual section keeps one identity across versions.
func stableKeyFor(pathTitles []string) string {
	sum := sha256.Sum256([]byte(strings.Join(pathTitles, "\x1f")))
	return "sec-" + hex.EncodeToString(sum[:])[:16]
}

var inlineLinkPattern = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// cleanInlineMarkdown removes the inline decorations the snapshot strips from
// heading titles, keeping the human-readable text.
func cleanInlineMarkdown(title string) string {
	title = inlineLinkPattern.ReplaceAllString(title, "$1")
	title = strings.NewReplacer("**", "", "__", "", "`", "", "~~", "").Replace(title)
	return strings.TrimSpace(title)
}

// utf16Len reports the number of UTF-16 code units in s. The browser reader
// slices markdown with JavaScript string offsets, which are UTF-16 based.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// excerptFor builds the short preview shown in material lists: the first few
// level-2 headings joined by a middle dot, truncated to a readable length.
func excerptFor(sections []Section, limit int) string {
	if limit <= 0 {
		limit = 80
	}
	parts := make([]string, 0, 4)
	for _, section := range sections {
		if section.Level != 2 || strings.TrimSpace(section.Title) == "" {
			continue
		}
		parts = append(parts, section.Title)
		if len(parts) == 4 {
			break
		}
	}
	if len(parts) == 0 {
		for _, section := range sections {
			if strings.TrimSpace(section.Title) != "" {
				parts = append(parts, section.Title)
				break
			}
		}
	}
	excerpt := strings.Join(parts, " · ")
	if utf8.RuneCountInString(excerpt) > limit {
		runes := []rune(excerpt)
		excerpt = string(runes[:limit])
	}
	return excerpt
}

// minutesFor estimates reading time at roughly 500 characters per minute.
func minutesFor(chars int) int {
	if chars <= 0 {
		return 0
	}
	minutes := (chars + 499) / 500
	if minutes < 1 {
		minutes = 1
	}
	return minutes
}
