package upstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lobsterai2api/internal/auth"
)

func withTestServer(t *testing.T, h http.Handler) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	t.Setenv("LB2A_UPSTREAM_BASE", ts.URL)
	t.Setenv("LB2A_UPDATE_API", ts.URL+"/update")
	resetVersionCache()
	return New()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func envelope(data any) map[string]any {
	return map[string]any{"code": 0, "msg": "ok", "data": data}
}

func TestDailyCheckinSuccess(t *testing.T) {
	var gotUA, gotVersion, gotIdem string
	mux := http.NewServeMux()
	mux.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"code": 0,
			"data": map[string]any{"value": map[string]any{"version": "2026.9.4"}},
		})
	})
	mux.HandleFunc("/api/client-activities/slot", func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotVersion = r.URL.Query().Get("clientVersion")
		writeJSON(w, 200, envelope(map[string]any{
			"slotState": "available",
			"activity": map[string]any{
				"activityCode":   "daily_check_in",
				"configRevision": 7,
			},
		}))
	})
	mux.HandleFunc("/api/client-activities/daily_check_in/context", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("configRevision") != "7" {
			writeJSON(w, 200, map[string]any{"code": 1, "msg": "bad rev"})
			return
		}
		writeJSON(w, 200, envelope(map[string]any{
			"state":   map[string]any{"claimedToday": false},
			"actions": []string{"check_in"},
		}))
	})
	mux.HandleFunc("/api/client-activities/daily_check_in/actions/check_in", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if fmtSprint := body["idempotencyKey"]; fmtSprint != nil {
			gotIdem, _ = fmtSprint.(string)
		}
		if body["configRevision"] != float64(7) {
			t.Errorf("configRevision = %v", body["configRevision"])
		}
		writeJSON(w, 200, envelope(map[string]any{
			"result": map[string]any{"creditsGranted": 100},
		}))
	})

	c := withTestServer(t, mux)
	res, err := c.DailyCheckin(&auth.Auth{AccessToken: "tok", UID: "1"})
	if err != nil {
		t.Fatalf("DailyCheckin: %v", err)
	}
	if res.Message != "签到成功" {
		t.Fatalf("Message = %q", res.Message)
	}
	if res.Gained == nil || *res.Gained != 100 {
		t.Fatalf("Gained = %v", res.Gained)
	}
	if gotUA != "LobsterAI/2026.9.4" || gotVersion != "2026.9.4" {
		t.Fatalf("UA=%q version=%q", gotUA, gotVersion)
	}
	if gotIdem == "" {
		t.Fatal("missing idempotencyKey")
	}
}

func TestDailyCheckinAlreadyClaimed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"code": 0,
			"data": map[string]any{"value": map[string]any{"version": "2026.9.4"}},
		})
	})
	mux.HandleFunc("/api/client-activities/slot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, envelope(map[string]any{
			"slotState": "available",
			"activity":  map[string]any{"activityCode": "daily_check_in", "configRevision": 1},
		}))
	})
	mux.HandleFunc("/api/client-activities/daily_check_in/context", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, envelope(map[string]any{
			"state":   map[string]any{"claimedToday": true},
			"actions": []string{},
		}))
	})
	c := withTestServer(t, mux)
	res, err := c.DailyCheckin(&auth.Auth{AccessToken: "tok"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Message != "今天已签到，跳过" {
		t.Fatalf("Message = %q", res.Message)
	}
}

func TestDailyCheckinNoSlot(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"code": 0,
			"data": map[string]any{"value": map[string]any{"version": "2026.9.4"}},
		})
	})
	mux.HandleFunc("/api/client-activities/slot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, envelope(map[string]any{"slotState": "empty"}))
	})
	c := withTestServer(t, mux)
	res, err := c.DailyCheckin(&auth.Auth{AccessToken: "tok"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(res.Message, "无可用活动") {
		t.Fatalf("Message = %q", res.Message)
	}
}

func TestResolveClientVersionBad(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"code": 0,
			"data": map[string]any{"value": map[string]any{"version": "not-a-ver"}},
		})
	})
	c := withTestServer(t, mux)
	_, err := c.ResolveClientVersion()
	if err == nil {
		t.Fatal("expected error")
	}
}
