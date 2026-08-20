package httpapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/remask/remask-core/internal/license"
	"github.com/remask/remask-core/internal/pii"
)

func TestLicenseStatusAndImportAPI(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manager := license.NewManager(t.TempDir(), "RMK1-TEST", nil, license.NewVerifier(map[string]ed25519.PublicKey{"test-v1": publicKey}))
	router := &Router{licenses: manager}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/license", router.getLicense)
	mux.HandleFunc("POST /api/v1/license/import", router.importLicense)

	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/v1/license", nil))
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"status":"missing"`) {
		t.Fatalf("initial license status: %d %s", statusResponse.Code, statusResponse.Body.String())
	}

	now := time.Now().UTC().Truncate(time.Second)
	data, err := license.Sign(privateKey, "test-v1", license.Claims{
		LicenseID: "lic_api_test", Product: license.Product, Edition: "professional", DeviceID: "RMK1-TEST",
		IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(24 * time.Hour), Features: []string{"pii-protection"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"content": string(data)})
	importResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/license/import", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(importResponse, request)
	if importResponse.Code != http.StatusOK || !strings.Contains(importResponse.Body.String(), `"status":"valid"`) {
		t.Fatalf("import license: %d %s", importResponse.Code, importResponse.Body.String())
	}
}

func TestLicenseImportAPIRejectsInvalidFile(t *testing.T) {
	manager := license.NewManager(t.TempDir(), "RMK1-TEST", nil, license.NewVerifier(nil))
	router := &Router{licenses: manager}
	body, _ := json.Marshal(map[string]string{"content": "not a license"})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/license/import", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	router.importLicense(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "LICENSE_FILE_INVALID") {
		t.Fatalf("invalid import: %d %s", response.Code, response.Body.String())
	}
}

func TestFreeLicenseRejectsSecondRuleIncludingPreset(t *testing.T) {
	manager := license.NewManager(t.TempDir(), "RMK1-TEST", nil, license.NewVerifier(nil))
	router := &Router{licenses: manager, rules: pii.NewRuleDetector()}
	body := `{"rules":[` +
		`{"id":"SECRET_KEY","pattern":"secret","enabled":true},` +
		`{"id":"ONE","pattern":"one","enabled":true}` +
		`]}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/policy", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.putPolicy(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "LICENSE_FEATURE_REQUIRED") {
		t.Fatalf("second free rule response: %d %s", response.Code, response.Body.String())
	}
}
