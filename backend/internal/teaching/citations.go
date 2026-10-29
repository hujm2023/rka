package teaching

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A native RAG citation becomes an RKA link only after matching an authorized,
// versioned knowledge base and the exact original filename uploaded to it.
func (s *Service) bindCitation(ctx context.Context, req TurnRequest, event Event) (Event, bool) {
	kb, _ := event.Payload["kbName"].(string)
	if kb == "" {
		return event, true
	}
	kb = strings.TrimPrefix(kb, "admin:kb:")
	allowed := false
	for _, name := range req.KnowledgeBases {
		if strings.TrimPrefix(name, "admin:kb:") == kb {
			allowed = true
		}
	}
	if !allowed {
		return Event{}, false
	}
	var material, version, title, rel string
	err := s.db.Pool.QueryRow(ctx, `select m.id,v.id::text,m.title,b.rel_path from knowledge_indexes k
  join material_versions v on v.id=k.material_version_id join materials m on m.id=v.material_id join blobs b on b.sha256=v.sha256
  where k.kb_name=$2 and k.status='ready' and `+knowledgeAccess, req.Identity.UserID, kb).Scan(&material, &version, &title, &rel)
	if err != nil {
		return Event{}, false
	}
	source, _ := event.Payload["sourcePath"].(string)
	if filepath.Base(source) != material+"-"+version+".md" {
		return Event{}, false
	}
	clean := filepath.Clean(strings.TrimPrefix(rel, "originals/"))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return Event{}, false
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.OriginalsDir(), clean))
	if err != nil {
		return Event{}, false
	}
	quote, _ := event.Payload["text"].(string)
	quote = strings.TrimSpace(quote)
	anchor := ""
	if quote != "" {
		offset := strings.Index(string(raw), quote)
		if offset < 0 {
			quote = ""
		} else {
			units := 0
			for _, r := range string(raw[:offset]) {
				units++
				if r > 0xffff {
					units++
				}
			}
			_ = s.db.Pool.QueryRow(ctx, `select anchor from material_sections where material_version_id=$1::uuid and start_offset<=$2 order by start_offset desc limit 1`, version, units).Scan(&anchor)
		}
	}
	hash := sha256.Sum256([]byte(kb + source + quote))
	event.Payload = map[string]any{"sourceId": fmt.Sprintf("R%x", hash[:5]), "materialId": material, "materialVersionId": version, "sectionId": anchor, "title": title, "text": quote}
	return event, true
}

func (s *Service) bindFrames(ctx context.Context, req TurnRequest, events []Event) []Event {
	result := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Type == "citation" {
			var ok bool
			event, ok = s.bindCitation(ctx, req, event)
			if !ok {
				continue
			}
		}
		result = append(result, event)
	}
	return result
}
