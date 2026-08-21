package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestSessionOwnershipRegistry_SameOwnerIsIdempotent(t *testing.T) {
	root := t.TempDir()
	registry := NewSessionOwnershipRegistry(filepath.Join(root, "owners.json"))
	owner := AgentSessionOwner{
		Project:           "project-a",
		StorePath:         filepath.Join(root, "sessions.json"),
		SessionKey:        "feishu:chat:user-a",
		InternalSessionID: "s1",
	}
	scope := filepath.Join(root, ".codex")
	if err := registry.Claim(scope, "thread-1", owner); err != nil {
		t.Fatalf("first Claim() error = %v", err)
	}
	owner.InternalSessionID = "s2"
	if err := registry.Claim(scope, "thread-1", owner); err != nil {
		t.Fatalf("same-owner Claim() error = %v", err)
	}
}

func TestSessionOwnershipRegistry_DifferentOwnerConflictsAndPersists(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owners.json")
	scope := filepath.Join(root, ".codex")
	ownerA := AgentSessionOwner{Project: "project-a", StorePath: filepath.Join(root, "a.json"), SessionKey: "chat-a"}
	ownerB := AgentSessionOwner{Project: "project-b", StorePath: filepath.Join(root, "b.json"), SessionKey: "chat-b"}

	if err := NewSessionOwnershipRegistry(path).Claim(scope, "thread-1", ownerA); err != nil {
		t.Fatalf("Claim(ownerA) error = %v", err)
	}

	err := NewSessionOwnershipRegistry(path).Claim(scope, "thread-1", ownerB)
	var conflict *SessionOwnershipConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Claim(ownerB) error = %v, want SessionOwnershipConflictError", err)
	}
	if conflict.Existing.Project != ownerA.Project || conflict.Existing.SessionKey != ownerA.SessionKey {
		t.Fatalf("existing owner = %+v, want %+v", conflict.Existing, ownerA)
	}
}

func TestSessionOwnershipRegistry_ConcurrentClaimHasSingleWinner(t *testing.T) {
	root := t.TempDir()
	registry := NewSessionOwnershipRegistry(filepath.Join(root, "owners.json"))
	scope := filepath.Join(root, ".codex")

	const claimers = 16
	var wg sync.WaitGroup
	results := make(chan error, claimers)
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- registry.Claim(scope, "thread-1", AgentSessionOwner{
				Project:    "project",
				StorePath:  filepath.Join(root, "sessions.json"),
				SessionKey: "chat-" + string(rune('a'+i)),
			})
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case IsSessionOwnershipConflict(err):
			conflicts++
		default:
			t.Fatalf("unexpected Claim() error = %v", err)
		}
	}
	if successes != 1 || conflicts != claimers-1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/%d", successes, conflicts, claimers-1)
	}
}

func TestSessionOwnershipRegistry_CrossProcessClaimHasSingleWinner(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owners.json")
	scope := filepath.Join(root, ".codex")

	const claimers = 8
	type result struct {
		err    error
		output string
	}
	results := make(chan result, claimers)
	for i := 0; i < claimers; i++ {
		go func(i int) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSessionOwnershipRegistry_ProcessHelper$")
			cmd.Env = append(os.Environ(),
				"CC_OWNERSHIP_HELPER=1",
				"CC_OWNERSHIP_PATH="+path,
				"CC_OWNERSHIP_SCOPE="+scope,
				"CC_OWNERSHIP_OWNER="+strconv.Itoa(i),
			)
			out, err := cmd.CombinedOutput()
			results <- result{err: err, output: string(out)}
		}(i)
	}

	successes := 0
	conflicts := 0
	for i := 0; i < claimers; i++ {
		result := <-results
		if result.err == nil {
			successes++
			continue
		}
		var exitErr *exec.ExitError
		if errors.As(result.err, &exitErr) && exitErr.ExitCode() == 3 {
			conflicts++
			continue
		}
		t.Fatalf("claim helper error = %v, output=%s", result.err, result.output)
	}
	if successes != 1 || conflicts != claimers-1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/%d", successes, conflicts, claimers-1)
	}
}

func TestSessionOwnershipRegistry_ProcessHelper(t *testing.T) {
	if os.Getenv("CC_OWNERSHIP_HELPER") != "1" {
		return
	}
	err := NewSessionOwnershipRegistry(os.Getenv("CC_OWNERSHIP_PATH")).Claim(
		os.Getenv("CC_OWNERSHIP_SCOPE"),
		"thread-1",
		AgentSessionOwner{
			Project:    "project",
			StorePath:  filepath.Join(filepath.Dir(os.Getenv("CC_OWNERSHIP_PATH")), "sessions.json"),
			SessionKey: "chat-" + os.Getenv("CC_OWNERSHIP_OWNER"),
		},
	)
	if IsSessionOwnershipConflict(err) {
		os.Exit(3)
	}
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
}
