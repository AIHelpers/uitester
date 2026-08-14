package usecase_test

import (
	"testing"

	"uitester/internal/usecase"
)

func TestToolRegistry_RegisterAndResolve(t *testing.T) {
	registry := usecase.NewToolRegistry()
	driver := newFakeDriver()

	if err := registry.Register(fakeConnector{name: "fake", driver: driver}); err != nil {
		t.Fatalf("register: %v", err)
	}

	c, err := registry.Resolve("fake")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if c.Name() != "fake" {
		t.Fatalf("expected name 'fake', got %q", c.Name())
	}
}

func TestToolRegistry_DuplicateNameRejected(t *testing.T) {
	registry := usecase.NewToolRegistry()
	driver := newFakeDriver()

	if err := registry.Register(fakeConnector{name: "fake", driver: driver}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := registry.Register(fakeConnector{name: "fake", driver: driver}); err == nil {
		t.Fatal("expected error registering duplicate tool name, got nil")
	}
}

func TestToolRegistry_UnknownName(t *testing.T) {
	registry := usecase.NewToolRegistry()
	if _, err := registry.Resolve("nope"); err == nil {
		t.Fatal("expected error resolving unknown tool name, got nil")
	}
}
