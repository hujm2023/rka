package ops

import (
	"strings"
	"testing"
)

func TestSafeJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"materials/abc.md", "a/b/c.json", "secret-key"} {
		if _, err := safeJoin(root, rel); err != nil {
			t.Fatalf("safeJoin rejected a legitimate path %q: %v", rel, err)
		}
	}
	for _, rel := range []string{"", ".", "..", "../escape", "a/../../escape", "/etc/passwd", "a/../../../../tmp/x"} {
		if _, err := safeJoin(root, rel); err == nil {
			t.Fatalf("safeJoin accepted traversal path %q", rel)
		}
	}
}

// The password must never appear in a command line visible through ps/proc.
func TestCommandDSNExcludesPassword(t *testing.T) {
	conn := connInfo{
		User: "rka", Password: "pass",
		Host: "db.internal", Port: "5432", Database: "rka", SSLMode: "disable",
	}
	commandDSN := conn.dsnForCommand("rka_verify_x")
	if strings.Contains(commandDSN, conn.Password) {
		t.Fatalf("command DSN leaked the password: %s", commandDSN)
	}
	if !strings.HasPrefix(commandDSN, "postgres://rka@db.internal:5432/rka_verify_x") {
		t.Fatalf("unexpected command DSN: %s", commandDSN)
	}
	// The password is still available to the child process through PGPASSWORD.
	env := strings.Join(conn.env(), "\n")
	if !strings.Contains(env, "PGPASSWORD="+conn.Password) {
		t.Fatalf("PGPASSWORD is missing from the child process environment")
	}
	// pgx may keep the password in memory, but only the argv form is audited.
	if !strings.Contains(conn.dsnFor("rka_verify_x"), conn.Password) {
		t.Fatalf("in-memory DSN should still carry the password for pgx")
	}
}
