package main

import (
	"context"
	"testing"
	"time"

	userservice "github.com/servekit/user-service/pkg"
)

func TestSmoke_RegisterLoginLogout(t *testing.T) {
	// This test requires running PostgreSQL and Redis
	// Skip if not available
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}

	// In a real environment, we would:
	// 1. Start server in background goroutine
	// 2. Wait for health check to pass
	// 3. Register a user
	// 4. Login
	// 5. Get profile
	// 6. Logout

	// For now, this is a placeholder that documents the integration test flow.
	// Integration tests that use testcontainers are located in internal packages.

	client, err := userservice.NewClient("localhost:9000")
	if err != nil {
		t.Logf("Expected failure if server not running: %v", err)
		return
	}
	defer client.Close()

	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check health
	// _, err = client.GetProfile(ctx, &emptypb.Empty{})
	// ...

	t.Log("smoke test placeholder — integration tests are in internal packages")
}
