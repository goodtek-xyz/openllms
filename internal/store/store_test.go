package store_test

import (
	"testing"

	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/google/uuid"
)

func TestAuthorizeRoute(t *testing.T) {
	s := &store.Store{}
	pid := uuid.New()
	rid := uuid.New()
	other := uuid.New()
	rt := &store.Route{ID: rid, ProjectID: pid}

	ac := &store.AuthContext{ProjectID: pid}
	if err := s.AuthorizeRoute(ac, rt); err != nil {
		t.Fatal(err)
	}
	ac.RouteID = &rid
	if err := s.AuthorizeRoute(ac, rt); err != nil {
		t.Fatal(err)
	}
	ac.RouteID = &other
	if err := s.AuthorizeRoute(ac, rt); err == nil {
		t.Fatal("expected forbidden")
	}
	ac = &store.AuthContext{ProjectID: uuid.New()}
	if err := s.AuthorizeRoute(ac, rt); err == nil {
		t.Fatal("expected forbidden project")
	}
}
