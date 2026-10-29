package auth_test

import (
	"net/http"
	"sort"
	"sync"
	"testing"

	"rka/internal/testutil"
)

func TestRejectStaleBrowserAccount(t *testing.T) {
	application, server, _ := testutil.NewApp(t)
	a := testutil.CreateUser(t, application, "page-a@example.com", "correct-horse-battery", "learner")
	testutil.CreateUser(t, application, "page-b@example.com", "correct-horse-battery", "learner")
	testutil.Login(t, server, "page-b@example.com", "correct-horse-battery")
	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/practice/stats", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-RKA-User-ID", a.ID)
	response, err := server.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale page got %d, want 409", response.StatusCode)
	}
}

func TestConcurrentAdminDemotionsLeaveOneAdmin(t *testing.T) {
	app, a, db := testutil.NewApp(t)
	b := testutil.NewServer(t, app.Handler)
	ua := testutil.CreateUser(t, app, "admin-a@example.com", "correct-horse-battery", "admin")
	ub := testutil.CreateUser(t, app, "admin-b@example.com", "correct-horse-battery", "admin")
	testutil.Login(t, a, ua.Email, "correct-horse-battery")
	testutil.Login(t, b, ub.Email, "correct-horse-battery")
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i, server := range []*testutil.Server{a, b} {
		id := []string{ua.ID, ub.ID}[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, _ := server.Do(t, http.MethodPatch, "/api/admin/users/"+id, map[string]any{"role": "learner"})
			statuses <- code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	codes := []int{}
	for code := range statuses {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	if len(codes) != 2 || codes[0] != 200 || codes[1] != 409 {
		t.Fatalf("demotions %v, want [200 409]", codes)
	}
	var admins int
	if err := db.Pool.QueryRow(t.Context(), `select count(*) from users where role='admin' and status='active'`).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Fatalf("remaining admins: %d", admins)
	}
}
