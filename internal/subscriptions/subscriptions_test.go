package subscriptions

import (
	"encoding/json"
	"strings"
	"testing"

	"vetka-backend-panel/internal/users"
)

func TestBuildNaiveURI(t *testing.T) {
	uri := BuildNaiveURI(users.AccessWithNode{
		Access:                   users.Access{ProtocolUsername: "demo", ProtocolPassword: "secret"},
		NodeName:                 "Node One",
		NodeDomain:               "example.com",
		NodeProtocolSettingsJSON: []byte(`{"naive":{"port":443}}`),
	})
	if !strings.HasPrefix(uri, "naive+https://demo:secret@example.com:443") {
		t.Fatalf("unexpected naive uri: %s", uri)
	}
}

func TestBuildMieruURI(t *testing.T) {
	uri := BuildMieruURI(users.AccessWithNode{
		Access:                   users.Access{ProtocolUsername: "demo user", ProtocolPassword: "sec/ret"},
		NodeName:                 "Test Mieru Node",
		NodeDomain:               "chrono.vetka.tech",
		NodeProtocolSettingsJSON: []byte(`{"mieru":{"ports":["2012-2022","2030-2040"],"protocol":"TCP","mtu":1400,"multiplexing":"MULTIPLEXING_HIGH","handshake_mode":"HANDSHAKE_NO_WAIT","profile":"Test Mieru Node"}}`),
	}, false)
	if !strings.HasPrefix(uri, "mierus://") {
		t.Fatalf("expected mierus scheme, got %s", uri)
	}
	if strings.Contains(strings.ToLower(uri), "todo mieru") {
		t.Fatalf("placeholder remained in uri: %s", uri)
	}
	for _, want := range []string{
		"chrono.vetka.tech",
		"port=2012-2022",
		"port=2030-2040",
		"protocol=TCP",
		"mtu=1400",
		"multiplexing=MULTIPLEXING_HIGH",
		"handshake-mode=HANDSHAKE_NO_WAIT",
	} {
		if !strings.Contains(uri, want) {
			t.Fatalf("missing %q in %s", want, uri)
		}
	}
	if strings.Contains(uri, " ") {
		t.Fatalf("uri contains raw spaces: %s", uri)
	}
	if !strings.Contains(uri, "demo%20user") || !strings.Contains(uri, "sec%2Fret") {
		t.Fatalf("credentials were not escaped correctly: %s", uri)
	}
	if strings.Count(uri, "port=") != 2 {
		t.Fatalf("expected repeated port params, got %s", uri)
	}
	if strings.Count(uri, "protocol=") != 2 {
		t.Fatalf("expected protocol count to match port count, got %s", uri)
	}
}

func TestBuildSubscriptionJSON(t *testing.T) {
	body, contentType, err := BuildSubscription(testAssignments(), "json", false)
	if err != nil {
		t.Fatalf("BuildSubscription returned error: %v", err)
	}
	if contentType != "application/json; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", contentType)
	}
	if strings.Contains(strings.ToLower(body), "todo") {
		t.Fatalf("json contains placeholder text: %s", body)
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	logCfg := cfg["log"].(map[string]any)
	if logCfg["level"] != "info" {
		t.Fatalf("unexpected log level: %#v", logCfg["level"])
	}
	if _, ok := cfg["dns"].(map[string]any); !ok {
		t.Fatalf("dns section missing")
	}
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok {
		t.Fatalf("outbounds missing")
	}
	var (
		foundSelector bool
		foundNaive    bool
		foundMieru    bool
	)
	for _, item := range outbounds {
		ob := item.(map[string]any)
		switch ob["type"] {
		case "selector":
			foundSelector = ob["tag"] == "proxy"
			if got := len(ob["outbounds"].([]any)); got < 2 {
				t.Fatalf("selector should contain proxy tags, got %d", got)
			}
		case "naive":
			foundNaive = true
			if ob["server"] != "alps.vetka.tech" || ob["server_port"] != float64(443) {
				t.Fatalf("unexpected naive outbound: %#v", ob)
			}
			if ob["username"] != "easewa" || ob["password"] != "as3231e2" {
				t.Fatalf("missing naive credentials: %#v", ob)
			}
			if ob["quic"] != false {
				t.Fatalf("expected quic=false: %#v", ob)
			}
			tlsCfg := ob["tls"].(map[string]any)
			if tlsCfg["enabled"] != true || tlsCfg["server_name"] != "alps.vetka.tech" {
				t.Fatalf("unexpected tls config: %#v", tlsCfg)
			}
		case "mieru":
			foundMieru = true
			if ob["server"] != "chrono.vetka.tech" || ob["server_port"] != float64(2012) {
				t.Fatalf("unexpected mieru outbound: %#v", ob)
			}
			if ob["transport"] != "TCP" || ob["multiplexing"] != "MULTIPLEXING_HIGH" {
				t.Fatalf("unexpected mieru transport config: %#v", ob)
			}
			if ob["username"] != "user" || ob["password"] != "password" {
				t.Fatalf("missing mieru credentials: %#v", ob)
			}
		}
	}
	if !foundSelector || !foundNaive || !foundMieru {
		t.Fatalf("missing expected outbounds: selector=%v naive=%v mieru=%v", foundSelector, foundNaive, foundMieru)
	}
	routeCfg := cfg["route"].(map[string]any)
	if routeCfg["final"] != "proxy" {
		t.Fatalf("unexpected route.final: %#v", routeCfg["final"])
	}
}

func TestBuildSubscriptionRawFormats(t *testing.T) {
	rawBody, rawType, err := BuildSubscriptionWithMetadata(testAssignments(), "raw", false, DefaultProfileTitle, DefaultUpdateIntervalHours)
	if err != nil {
		t.Fatalf("raw build error: %v", err)
	}
	if rawType != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected raw content type: %s", rawType)
	}
	if !strings.Contains(rawBody, "naive+https://") || !strings.Contains(rawBody, "naive://") || !strings.Contains(rawBody, "mierus://") {
		t.Fatalf("raw output missing expected links: %s", rawBody)
	}
	if !strings.Contains(rawBody, "//profile-title: base64:"+ProfileTitleBase64(DefaultProfileTitle)) {
		t.Fatalf("raw output missing profile metadata: %s", rawBody)
	}
	if strings.Contains(strings.ToLower(rawBody), "todo") {
		t.Fatalf("raw output contains placeholder text: %s", rawBody)
	}

	mieruBody, mieruType, err := BuildSubscription(testAssignments(), "mierus", false)
	if err != nil {
		t.Fatalf("mierus build error: %v", err)
	}
	if mieruType != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected mierus content type: %s", mieruType)
	}
	if !strings.HasPrefix(mieruBody, "mierus://") {
		t.Fatalf("expected mierus output, got %s", mieruBody)
	}

	naiveBody, naiveType, err := BuildSubscription(testAssignments(), "naive", false)
	if err != nil {
		t.Fatalf("naive build error: %v", err)
	}
	if naiveType != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected naive content type: %s", naiveType)
	}
	if !strings.HasPrefix(naiveBody, "naive://") {
		t.Fatalf("expected naive output, got %s", naiveBody)
	}
	if strings.Contains(naiveBody, "mierus://") {
		t.Fatalf("naive-only output should not contain mieru links: %s", naiveBody)
	}

	hiddifyBody, hiddifyType, err := BuildSubscriptionWithMetadata(testAssignments(), "hiddify", false, DefaultProfileTitle, DefaultUpdateIntervalHours)
	if err != nil {
		t.Fatalf("hiddify build error: %v", err)
	}
	if hiddifyType != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected hiddify content type: %s", hiddifyType)
	}
	if !strings.Contains(hiddifyBody, "mierus://") || !strings.Contains(hiddifyBody, "naive://") {
		t.Fatalf("hiddify output missing expected links: %s", hiddifyBody)
	}
	if strings.Contains(hiddifyBody, "\"outbounds\"") {
		t.Fatalf("hiddify output should not contain json: %s", hiddifyBody)
	}
	if strings.Contains(strings.ToLower(hiddifyBody), "todo") {
		t.Fatalf("hiddify output contains placeholder text: %s", hiddifyBody)
	}
}

func TestBuildSubscriptionHiddifyJSON(t *testing.T) {
	body, contentType, err := BuildSubscription(testAssignments(), "hiddify-json", false)
	if err != nil {
		t.Fatalf("hiddify-json build error: %v", err)
	}
	if contentType != "application/json; charset=utf-8" {
		t.Fatalf("unexpected hiddify-json content type: %s", contentType)
	}
	if strings.Contains(strings.ToLower(body), "todo") {
		t.Fatalf("hiddify-json contains placeholder text: %s", body)
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("invalid hiddify-json: %v", err)
	}
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok || len(outbounds) < 2 {
		t.Fatalf("hiddify-json outbounds missing: %#v", cfg["outbounds"])
	}
	var foundMieru, foundNaive bool
	for _, item := range outbounds {
		ob := item.(map[string]any)
		switch ob["type"] {
		case "mieru":
			foundMieru = true
			if ob["server_port"] != float64(0) {
				t.Fatalf("unexpected mieru server_port: %#v", ob["server_port"])
			}
			if _, hasTransport := ob["transport"]; hasTransport {
				t.Fatalf("hiddify-json mieru must not contain transport: %#v", ob)
			}
			bindings, ok := ob["portBindings"].([]any)
			if !ok || len(bindings) == 0 {
				t.Fatalf("hiddify-json mieru missing portBindings: %#v", ob)
			}
		case "naive":
			foundNaive = true
			if ob["server"] != "alps.vetka.tech" || ob["server_port"] != float64(443) {
				t.Fatalf("unexpected naive outbound: %#v", ob)
			}
			if ob["username"] != "easewa" || ob["password"] != "as3231e2" {
				t.Fatalf("missing naive credentials: %#v", ob)
			}
			if ob["udp_over_tcp"] != false {
				t.Fatalf("expected udp_over_tcp=false: %#v", ob)
			}
			tlsCfg := ob["tls"].(map[string]any)
			if tlsCfg["enabled"] != true {
				t.Fatalf("expected tls.enabled=true: %#v", tlsCfg)
			}
			if _, hasServerName := tlsCfg["server_name"]; hasServerName {
				t.Fatalf("hiddify-json naive should not contain tls.server_name: %#v", tlsCfg)
			}
			if _, hasQuic := ob["quic"]; hasQuic {
				t.Fatalf("hiddify-json naive should not contain quic: %#v", ob)
			}
		}
	}
	if !foundMieru || !foundNaive {
		t.Fatalf("missing expected hiddify-json outbounds: mieru=%v naive=%v", foundMieru, foundNaive)
	}
}

func TestContentDispositionFilename(t *testing.T) {
	if got := ContentDispositionFilename("json"); got != "vetka-vpn.json" {
		t.Fatalf("unexpected json filename: %s", got)
	}
	if got := ContentDispositionFilename("hiddify"); got != "vetka-vpn.txt" {
		t.Fatalf("unexpected text filename: %s", got)
	}
}

func TestProfileTitleBase64(t *testing.T) {
	if got := ProfileTitleBase64(DefaultProfileTitle); got != "0JLQtdGC0LrQsCBWUE4=" {
		t.Fatalf("unexpected profile title base64: %s", got)
	}
}

func testAssignments() []users.AccessWithNode {
	return []users.AccessWithNode{
		{
			Access:                   users.Access{NodeID: "naive-node", ProtocolUsername: "easewa", ProtocolPassword: "as3231e2"},
			AgentNodeID:              "f755b1",
			NodeName:                 "Alps Node",
			NodeDomain:               "alps.vetka.tech",
			NodeProtocolType:         "naive",
			NodeProtocolSettingsJSON: []byte(`{"naive":{"port":443}}`),
		},
		{
			Access:                   users.Access{NodeID: "mieru-node", ProtocolUsername: "user", ProtocolPassword: "password"},
			AgentNodeID:              "test-mieru-1",
			NodeName:                 "Chrono Node",
			NodeDomain:               "chrono.vetka.tech",
			NodeProtocolType:         "mieru",
			NodeProtocolSettingsJSON: []byte(`{"mieru":{"ports":["2012-2022"],"protocol":"TCP","mtu":1400,"multiplexing":"MULTIPLEXING_HIGH","handshake_mode":"HANDSHAKE_NO_WAIT"}}`),
		},
	}
}
