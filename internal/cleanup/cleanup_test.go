package cleanup

import (
	"os"
	"testing"
)

func TestStack(t *testing.T) {
	f1, err := os.CreateTemp("", "cleanup-test-*")
	if err != nil {
		t.Fatal(err)
	}
	f1.Close()
	f2, err := os.CreateTemp("", "cleanup-test-*")
	if err != nil {
		t.Fatal(err)
	}
	f2.Close()

	var s Stack
	s.Push(f1.Name())
	s.Push(f2.Name())

	// Pop f2 so it should survive
	s.Pop()

	// Cleanup should only remove f1
	s.Cleanup()

	if _, err := os.Stat(f1.Name()); !os.IsNotExist(err) {
		t.Errorf("f1 should have been removed")
	}
	if _, err := os.Stat(f2.Name()); os.IsNotExist(err) {
		t.Errorf("f2 should still exist")
	}

	// Clean up f2 manually
	_ = os.Remove(f2.Name())
}

func TestCleanupEmpty(t *testing.T) {
	var s Stack
	s.Cleanup() // should not panic
}
