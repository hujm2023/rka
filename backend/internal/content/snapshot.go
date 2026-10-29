package content

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// snapshotCatalog mirrors content/catalog.json. Only the fields the
// importer needs are typed; subjective items and figures stay as raw JSON so
// the payload survives a round trip unchanged.
type snapshotCatalog struct {
	Version        string                    `json:"version"`
	Choices        []snapshotQuestion        `json:"choices"`
	Cases          []json.RawMessage         `json:"cases"`
	Essays         []json.RawMessage         `json:"essays"`
	Lessons        []snapshotLesson          `json:"lessons"`
	Materials      []snapshotMaterial        `json:"materials"`
	KnowledgeLinks map[string][]snapshotLink `json:"knowledgeLinks"`
}

type snapshotQuestion struct {
	ID            string            `json:"id"`
	SourceType    string            `json:"sourceType"`
	Term          string            `json:"term"`
	Paper         string            `json:"paper"`
	QuestionNo    int               `json:"questionNo"`
	Module        string            `json:"module"`
	Knowledge     string            `json:"knowledge"`
	Difficulty    string            `json:"difficulty"`
	Stem          string            `json:"stem"`
	Options       map[string]string `json:"options"`
	Answer        string            `json:"answer"`
	Analysis      string            `json:"analysis"`
	SourceFile    string            `json:"sourceFile"`
	SourceURL     string            `json:"sourceUrl"`
	SourceLabel   string            `json:"sourceLabel"`
	Figure        json.RawMessage   `json:"figure"`
	Revision      string            `json:"revision"`
	FigureMissing bool              `json:"figureMissing"`
	MultiBlank    bool              `json:"multiBlank"`
}

type snapshotMaterial struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Book       string            `json:"book"`
	Kind       string            `json:"kind"`
	Module     string            `json:"module"`
	SourceFile string            `json:"sourceFile"`
	SourceURL  string            `json:"sourceUrl"`
	License    string            `json:"license"`
	Chars      int               `json:"chars"`
	Minutes    int               `json:"minutes"`
	Sections   []snapshotSection `json:"sections"`
	SourceHash string            `json:"sourceHash"`
	Excerpt    string            `json:"excerpt"`
}

type snapshotSection struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Level int    `json:"level"`
	Start int    `json:"start"`
}

// snapshotLesson is one legacy lesson index entry. It carries no body: the
// markdown lives in <snapshot>/lessons/<id>.md.
type snapshotLesson struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Book       string `json:"book"`
	Module     string `json:"module"`
	SourceFile string `json:"sourceFile"`
	SourceURL  string `json:"sourceUrl"`
	License    string `json:"license"`
	Chars      int    `json:"chars"`
	Minutes    int    `json:"minutes"`
	Excerpt    string `json:"excerpt"`
}

type snapshotLink struct {
	MaterialID   string   `json:"materialId"`
	SectionID    string   `json:"sectionId"`
	SectionTitle string   `json:"sectionTitle"`
	SectionPath  []string `json:"sectionPath"`
	Terms        []string `json:"terms"`
	Score        float64  `json:"score"`
}

// subjectiveMeta is the subset of a case/essay payload stored in columns.
type subjectiveMeta struct {
	ID         string `json:"id"`
	Revision   string `json:"revision"`
	Title      string `json:"title"`
	Module     string `json:"module"`
	Term       string `json:"term"`
	Paper      string `json:"paper"`
	SourceType string `json:"sourceType"`
	SourceURL  string `json:"sourceUrl"`
}

// loadSnapshot reads and validates the catalog file. It fails loudly when the
// snapshot is missing or malformed: a silent empty import would look like a
// successful one.
func loadSnapshot(snapshotDir string) (*snapshotCatalog, error) {
	path := filepath.Join(snapshotDir, "catalog.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read content catalog: %w", err)
	}
	var catalog snapshotCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("parse content catalog: %w", err)
	}
	if catalog.Version == "" {
		return nil, fmt.Errorf("content catalog has no version")
	}
	return &catalog, nil
}

// materialPath returns the markdown file that backs one catalog material.
func materialPath(snapshotDir, materialID string) string {
	return filepath.Join(snapshotDir, "materials", materialID+".md")
}

// lessonPath returns the markdown file that backs one legacy lesson entry.
func lessonPath(snapshotDir, lessonID string) string {
	return filepath.Join(snapshotDir, "lessons", lessonID+".md")
}
