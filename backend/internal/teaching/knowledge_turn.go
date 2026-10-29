package teaching

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// selectKnowledge narrows every turn against current RKA permissions. The
// newest validated version wins; rebuilding an index never removes old ones.
func (s *Service) selectKnowledge(ctx context.Context, userID string, scope map[string]any) ([]string, error) {
	material, _ := scope["materialId"].(string)
	version, _ := scope["materialVersionId"].(string)
	question, _ := scope["questionId"].(string)
	rows, err := s.db.Pool.Query(ctx, `select distinct on (m.id) k.kb_name from knowledge_indexes k
  join material_versions v on v.id=k.material_version_id join materials m on m.id=v.material_id
  where k.status='ready' and ($2='' or m.id=$2) and ($3='' or v.id::text=$3)
  and ($4='' or exists (select 1 from knowledge_links kl where kl.question_id=$4
    and kl.material_version_id=v.id and kl.status in ('auto','approved')))
  and `+knowledgeAccess+`
  order by m.id,v.version desc,k.created_at desc`, userID, material, version, question)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, "admin:kb:"+name)
	}
	return names, rows.Err()
}

func (s *Service) authorizeKnowledge(ctx context.Context, request TurnRequest) error {
	d, ok := s.deepTutor.(*deepTutorWSBackend)
	if !ok {
		return nil
	}
	if len(request.KnowledgeBases) == 0 && s.cfg.AI.Endpoint == "" {
		return nil
	}
	if err := s.ConfigureNative(ctx); err != nil {
		return err
	}
	userToken, err := d.userToken(ctx, request.Identity)
	if err != nil {
		return err
	}
	var status struct {
		UserID string `json:"user_id"`
	}
	if err := d.jsonCall(ctx, http.MethodGet, "/api/auth/status", nil, userToken, &status); err != nil {
		return err
	}
	if status.UserID == "" {
		return fmt.Errorf("DeepTutor 未返回独立用户标识，拒绝共享教学身份")
	}
	admin, err := d.adminToken(ctx)
	if err != nil {
		return err
	}
	bases := []any{}
	allNames, err := s.selectKnowledge(ctx, request.Identity.UserID, map[string]any{})
	if err != nil {
		return err
	}
	for _, name := range allNames {
		bases = append(bases, map[string]any{"name": strings.TrimPrefix(name, "admin:kb:")})
	}
	models := []any{}
	profile, model, err := d.activeModel(ctx, admin)
	if err != nil {
		return err
	}
	if profile != "" && model != "" {
		models = append(models, map[string]any{"profile_id": profile, "model_ids": []string{model}})
	}
	grant := map[string]any{"models": map[string]any{"llm": models}, "knowledge_bases": bases,
		"enabled_tools": []string{"rag", "kb_files"}, "mcp_tools": []string{}, "cli_apps": []string{}, "exec_enabled": false, "skills": []any{}, "partners": []any{}}
	return d.jsonCall(ctx, http.MethodPut, "/api/multi-user/users/"+url.PathEscape(status.UserID)+"/grants", map[string]any{"grant": grant}, admin, nil)
}
