package teaching_test

import (
	"testing"

	"rka/internal/teaching"
)

func TestQuestionRetrievesOnlyLinkedAccessibleVersion(t *testing.T) {
	service, db := newTestService(t, nil)
	user := seedUser(t, db, "scope@example.com")
	question := seedQuestion(t, db, "题目", "A", "解析")
	for _, sql := range []string{
		`insert into blobs(sha256,size,rel_path) values('test-sha',0,'unused')`,
		`insert into materials(id,title,book,kind,module,source_file) values('linked','关联','教材','textbook','architecture','unused'),('unrelated','其他','教材','textbook','architecture','unused')`,
		`insert into material_versions(material_id,version,sha256) values('linked',1,'test-sha'),('linked',2,'test-sha'),('unrelated',1,'test-sha')`,
		`insert into knowledge_indexes(material_version_id,fingerprint,kb_name,status) select id,'test',material_id||version::text,'ready' from material_versions`,
		`insert into material_sections(material_version_id,anchor,stable_key,title,level,start_offset) select id,'section','section','节',1,0 from material_versions where material_id='linked' and version=1`,
	} {
		if _, err := db.Pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		request, err := service.PrepareTurn(t.Context(), teaching.PrepareInput{UserID: user, Kind: "hint", Context: map[string]any{"questionId": question}})
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if len(request.KnowledgeBases) != 0 {
				t.Fatalf("unexpected whole-library search: %v", request.KnowledgeBases)
			}
			return
		}
		if len(request.KnowledgeBases) != 1 || request.KnowledgeBases[0] != want {
			t.Fatalf("scope=%v", request.KnowledgeBases)
		}
	}
	check("")
	if _, err := db.Pool.Exec(t.Context(), `insert into knowledge_links(question_id,material_id,material_version_id,section_id,section_stable_key,status) select $1,'linked',material_version_id,id,stable_key,'approved' from material_sections`, question); err != nil {
		t.Fatal(err)
	}
	check("admin:kb:linked1")
	if _, err := db.Pool.Exec(t.Context(), `update knowledge_links set status='rejected'`); err != nil {
		t.Fatal(err)
	}
	check("")
	if _, err := db.Pool.Exec(t.Context(), `update knowledge_links set status='approved'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `update materials set review_status='withdrawn' where id='linked'`); err != nil {
		t.Fatal(err)
	}
	check("")
}
