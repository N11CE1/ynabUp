//go:build container_smoke

package container_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func cleanupDocker(t *testing.T, args ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Errorf("cleanup docker %v: %v\n%s", args, err, out)
		}
	})
}

func certificates(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Container smoke test"},
		DNSNames:     []string{"api.ynab.com", "api.up.com.au"},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "api.crt"), filepath.Join(dir, "api.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestContainerRuntime(t *testing.T) {
	// No Compose or host env file is used. The application can only reach the
	// fixture on an internal network, with fake credentials and a throwaway DB.
	id := "ynabup-smoke-" + strings.ToLower(time.Now().Format("20060102t150405.000000000"))
	image, fixtureImage := id+":app", id+":fixture"
	docker(t, "build", "-t", image, ".")
	cleanupDocker(t, "image", "rm", image)
	docker(t, "build", "-f", "tests/container/Dockerfile", "-t", fixtureImage, ".")
	cleanupDocker(t, "image", "rm", fixtureImage)
	docker(t, "network", "create", "--internal", id)
	cleanupDocker(t, "network", "rm", id)
	docker(t, "volume", "create", id)
	cleanupDocker(t, "volume", "rm", id)

	fixture := id + "-api"
	docker(t, "create", "--name", fixture, "--network", id,
		"--network-alias", "api.ynab.com", "--network-alias", "api.up.com.au", fixtureImage)
	cleanupDocker(t, "rm", "-f", fixture)
	certPath, keyPath := certificates(t)
	docker(t, "cp", certPath, fixture+":/tmp/api.crt")
	docker(t, "cp", keyPath, fixture+":/tmp/api.key")
	docker(t, "start", fixture)

	// Wait for the fixture's TLS listener without requiring a host port.
	waitFor(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "docker", "exec", fixture, "wget", "--no-check-certificate",
			"--header", "Authorization: Bearer fake-up-token", "-q", "-O", "/dev/null", "https://api.up.com.au/api/v1/transactions").Run() == nil
	})

	createApp := func(t *testing.T, name string, args ...string) {
		t.Helper()
		command := []string{"create", "--name", name, "--network", id,
			"--mount", "type=volume,src=" + id + ",dst=/data",
			"--env", "DB_PATH=/data/sync.db", "--env", "SSL_CERT_FILE=/tmp/api.crt",
			"--env", "UP_API_TOKEN=fake-up-token", "--env", "YNAB_API_TOKEN=fake-ynab-token",
			"--env", "YNAB_BUDGET_ID=test-budget", "--env", `UP_ACCOUNT_MAP={"test-up":"test-account"}`,
			"--env", "UP_WEBHOOK_SECRET=fake-secret", "--env", "CRON_INTERVAL=1h",
			"--health-interval=1s", "--health-start-period=1s", "--health-timeout=2s", image}
		command = append(command, args...)
		docker(t, command...)
		cleanupDocker(t, "rm", "-f", name)
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("%s logs:\n%s", name, docker(t, "logs", name))
			}
		})
		docker(t, "cp", certPath, name+":/tmp/api.crt")
		docker(t, "start", name)
	}

	t.Run("one-shot exits successfully", func(t *testing.T) {
		name := id + "-once"
		createApp(t, name, "-serve=false")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "wait", name).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "0" {
			t.Fatalf("one-shot must exit successfully: %v, output %s", err, out)
		}
		logs := docker(t, "logs", name)
		if !strings.Contains(logs, "Done: 0 synced, 0 already synced (skipped), 0 failed") || strings.Contains(logs, "listening on") {
			t.Fatalf("expected one successful sync, not server mode:\n%s", logs)
		}
	})

	// Seed real sync state before simulating a root-run deployment. Use a
	// historical watermark so a reset to the current time cannot pass.
	const watermark = "2026-01-15T12:34:56Z"
	const record = "legacy-transfer|ynab-outflow|ynab-inflow"
	docker(t, "run", "--rm", "--network", "none", "--mount", "type=volume,src="+id+",dst=/data",
		"--entrypoint", "sqlite3", fixtureImage, "/data/sync.db",
		`INSERT INTO synced_transactions (import_id, ynab_transaction_id, linked_transfer_transaction_id)
		 VALUES ('legacy-transfer', 'ynab-outflow', 'ynab-inflow');
		 UPDATE settings SET value = '`+watermark+`' WHERE key = 'up_last_synced_at';`)
	if got := docker(t, "run", "--rm", "--network", "none", "--mount", "type=volume,src="+id+",dst=/data,readonly",
		"--entrypoint", "sqlite3", fixtureImage, "-readonly", "/data/sync.db",
		`SELECT value FROM settings WHERE key = 'up_last_synced_at';`); got != watermark {
		t.Fatalf("failed to seed exact watermark: got %q, want %q", got, watermark)
	}

	// Simulate the previous root-run deployment using the valid SQLite DB
	// created above, not an empty placeholder file. Keep restrictive modes.
	docker(t, "run", "--rm", "--network", "none", "--mount", "type=volume,src="+id+",dst=/data",
		"--entrypoint", "/bin/sh", image, "-ec",
		"test -s /data/sync.db; echo preserved > /data/marker; chown -R root:root /data; chmod 700 /data; chmod 600 /data/sync.db")

	beforeRestart := docker(t, "logs", fixture)
	t.Run("server repairs legacy volume and runs unprivileged", func(t *testing.T) {
		name := id + "-server"
		createApp(t, name) // Exercise the image's default CMD, without passing -serve.
		waitFor(t, func() bool {
			return docker(t, "inspect", "--format", "{{.State.Health.Status}}", name) == "healthy"
		})
		status := docker(t, "exec", name, "cat", "/proc/1/status")
		var uidLine string
		for _, line := range strings.Split(status, "\n") {
			if strings.HasPrefix(line, "Uid:") {
				uidLine = line
			}
		}
		fields := strings.Fields(uidLine)
		if len(fields) != 5 {
			t.Fatalf("missing process UID information: %s", status)
		}
		for _, uid := range fields[1:] {
			if uid == "0" {
				t.Fatalf("application PID 1 still has root UID: %s", uidLine)
			}
		}
		if cmd := docker(t, "exec", name, "cat", "/proc/1/cmdline"); !strings.HasPrefix(cmd, "/app/ynabup\x00") {
			t.Fatalf("application must be PID 1, got %q", cmd)
		}
		if marker := docker(t, "exec", "--user", "ynabup", name, "cat", "/data/marker"); marker != "preserved" {
			t.Fatalf("legacy volume contents changed: %q", marker)
		}
		// A health response alone doesn't prove reconciliation can write the DB.
		waitFor(t, func() bool {
			body := docker(t, "exec", name, "wget", "-qO-", "http://127.0.0.1:8080/healthz")
			var health struct {
				Status       string `json:"status"`
				LastSyncedAt string `json:"last_synced_at"`
			}
			if json.Unmarshal([]byte(body), &health) != nil || health.Status != "ok" {
				return false
			}
			lastSyncedAt, err := time.Parse(time.RFC3339, health.LastSyncedAt)
			seededAt, seedErr := time.Parse(time.RFC3339, watermark)
			return err == nil && seedErr == nil && lastSyncedAt.After(seededAt) && strings.Contains(docker(t, "logs", name), "Done: 0 synced, 0 already synced (skipped), 0 failed")
		})
		logs := docker(t, "logs", name)
		if strings.Contains(logs, "failed to") || strings.Contains(logs, "reconciliation failed") {
			t.Fatalf("expected successful startup reconciliation:\n%s", logs)
		}
		// Read the actual transaction row with the application's effective UID,
		// not root, after ownership repair and startup reconciliation.
		got := docker(t, "run", "--rm", "--network", "none", "--user", fields[2],
			"--mount", "type=volume,src="+id+",dst=/data,readonly",
			"--entrypoint", "sqlite3", fixtureImage, "-readonly", "/data/sync.db",
			`SELECT import_id, ynab_transaction_id, linked_transfer_transaction_id
			 FROM synced_transactions WHERE import_id = 'legacy-transfer';`)
		if got != record {
			t.Fatalf("legacy transaction and both YNAB IDs must survive restart: got %q, want %q", got, record)
		}
	})
	fixtureLogs := docker(t, "logs", fixture)
	if !strings.HasPrefix(fixtureLogs, beforeRestart) {
		t.Fatalf("fixture log history changed unexpectedly:\n%s", fixtureLogs)
	}
	requests := 0
	for _, line := range strings.Split(strings.TrimPrefix(fixtureLogs, beforeRestart), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 5 || fields[2] != "GET" || fields[3] != "api.up.com.au" {
			continue
		}
		requestURL, err := url.ParseRequestURI(fields[4])
		if err != nil {
			t.Fatalf("invalid Up request URI in fixture logs: %v", err)
		}
		if requestURL.Path != "/api/v1/transactions" {
			continue
		}
		requests++
		values, err := url.ParseQuery(requestURL.RawQuery)
		if err != nil {
			t.Fatalf("invalid Up request query: %v", err)
		}
		since := values["filter[since]"]
		if len(since) != 1 || since[0] != watermark {
			t.Fatalf("restart must fetch from the exact persisted watermark: got %v, want [%s]", since, watermark)
		}
	}
	if requests != 1 {
		t.Fatalf("expected exactly one Up transaction request after restart, got %d:\n%s", requests, fixtureLogs)
	}
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("timed out waiting for container readiness")
}
