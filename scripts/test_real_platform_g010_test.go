package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealPlatformRequiresBrowserIdentityAndCanariesBeforeReadiness(t *testing.T) {
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "test-real-platform")
	base := map[string]string{
		"BT_DEV_REAL_STACK_ACCEPTANCE":         "1",
		"FT_MODE":                              "test",
		"BT_DEV_REAL_USERNAME":                 "private-user",
		"BT_DEV_REAL_PASSWORD":                 "private-password",
		"BT_DEV_REAL_CREDENTIAL_CANARY":        "credential-canary",
		"BT_DEV_REAL_CREDENTIAL_SECRET_CANARY": "credential-secret-canary",
	}
	for _, missing := range []string{
		"BT_DEV_REAL_USERNAME",
		"BT_DEV_REAL_PASSWORD",
		"BT_DEV_REAL_CREDENTIAL_CANARY",
		"BT_DEV_REAL_CREDENTIAL_SECRET_CANARY",
	} {
		t.Run(missing, func(t *testing.T) {
			environment := []string{"PATH=" + os.Getenv("PATH")}
			for name, value := range base {
				if name != missing {
					environment = append(environment, name+"="+value)
				}
			}
			command := exec.Command(script)
			command.Dir = repo
			command.Env = environment
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("test-real accepted missing %s: %s", missing, output)
			}
			if !strings.Contains(string(output), missing+" is required") {
				t.Fatalf("missing %s failed at the wrong boundary: %s", missing, output)
			}
		})
	}
}

func TestRealPlatformOwnsOneSnapshotBoundBrowserCycle(t *testing.T) {
	repo := repoRoot(t)
	payload, err := os.ReadFile(filepath.Join(repo, "scripts", "test-real-platform"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, required := range []string{
		"validation-snapshot",
		"validation-compare",
		"test:e2e:real:harness",
		"test:e2e:real",
		"BT_DEV_REAL_TEST_HARNESS_ACCEPT=1",
		"BT_DEV_REAL_TEST_HARNESS_RESNAPSHOT_URL=http://127.0.0.1:13002/__bt/resnapshot",
		"BT_DEV_REAL_PLATFORM_URL=http://127.0.0.1:13002/",
		"browserSessionDigest",
		"capture_process_identities before",
		"capture_process_identities after",
		"cleanup_harness",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("test-real is missing %q", required)
		}
	}
	browserCycles := strings.Count(source, "--prefix web run test:e2e:real") - strings.Count(source, "--prefix web run test:e2e:real:harness")
	if browserCycles != 1 {
		t.Errorf("test-real must run exactly one Playwright cycle per invocation")
	}
	for _, service := range []string{"postgres", "gateway", "platform", "funding-worker", "web"} {
		if !strings.Contains(source, `capture_owned_identity "`+service+`"`) {
			t.Errorf("test-real does not capture %s ownership", service)
		}
	}
}

func TestRealPlatformPinsHarnessAndPlatformURLs(t *testing.T) {
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "test-real-platform")
	command := exec.Command(script)
	command.Dir = repo
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"BT_DEV_REAL_STACK_ACCEPTANCE=1",
		"FT_MODE=test",
		"BT_DEV_REAL_USERNAME=private-user",
		"BT_DEV_REAL_PASSWORD=private-password",
		"BT_DEV_REAL_CREDENTIAL_CANARY=credential-canary",
		"BT_DEV_REAL_CREDENTIAL_SECRET_CANARY=credential-secret-canary",
		"BT_DEV_REAL_PLATFORM_URL=http://127.0.0.1:19999/",
	}
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("test-real accepted a noncanonical browser target: %s", output)
	}
	if !strings.Contains(string(output), "BT_DEV_REAL_PLATFORM_URL must be http://127.0.0.1:13002/") {
		t.Fatalf("test-real rejected URL at the wrong boundary: %s", output)
	}
}
