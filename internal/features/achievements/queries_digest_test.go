package achievements

import (
	"context"
	"testing"
	"time"
)

func TestDigestKeepsTitlesAndStates(t *testing.T) {
	e := newEnv(t)
	user := e.CreateUser("digest@example.com")
	got, err := e.svc.Digest(context.Background(), user, LanguageFrom(""), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("want the catalog's achievements")
	}
	for _, item := range got {
		if item.Title == "" || item.State == "" {
			t.Fatalf("item = %+v", item)
		}
	}
}
