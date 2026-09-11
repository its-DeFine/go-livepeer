package server

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/livepeer/go-livepeer/ai/runner"
	"github.com/livepeer/go-livepeer/core"
	"github.com/stretchr/testify/require"
)

func TestLiveRunnerCoveredSessionAdmission(t *testing.T) {
	lp := newLiveRunnerHTTPOnchain(t)
	orch := lp.orchestrator.(*stubOrchestrator)
	orch.balances = make(map[ethcommon.Address]map[core.ManifestID]*big.Rat)
	orch.paymentCredit = big.NewRat(1, 1)

	admissionToken := strings.Repeat("ab", 24)
	callbackCalls := 0
	callbackStatus := http.StatusOK
	callbackAuthorized := true
	usedAdmissionTokens := make(map[string]bool)
	var callbackBody struct {
		Token        string                     `json:"token"`
		RunnerID     string                     `json:"runnerId"`
		PayerAddress string                     `json:"payerAddress"`
		Quote        runner.LiveRunnerPriceInfo `json:"quote"`
	}
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callbackCalls++
		if r.URL.Path != "/session-admission" {
			http.NotFound(w, r)
			return
		}
		token := r.Header.Get(runner.LiveRunnerSessionAdmissionHeader)
		if token == "" {
			t.Errorf("missing admission header")
			http.Error(w, "missing admission header", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&callbackBody); err != nil {
			t.Errorf("decode callback body: %v", err)
			http.Error(w, "bad callback body", http.StatusBadRequest)
			return
		}
		if callbackStatus != http.StatusOK {
			w.WriteHeader(callbackStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		authorized := callbackAuthorized && !usedAdmissionTokens[token]
		if authorized {
			usedAdmissionTokens[token] = true
		}
		if err := json.NewEncoder(w).Encode(map[string]bool{"authorized": authorized}); err != nil {
			t.Errorf("encode callback response: %v", err)
		}
	}))
	defer callback.Close()

	manager := lp.node.LiveRunnerManager.(*runner.LiveRunnerRegistry)
	_, err := manager.Heartbeat(runner.LiveRunnerHeartbeatRequest{
		RunnerID:             "covered-path-traversal",
		RunnerURL:            callback.URL + "/native",
		Status:               "ready",
		Mode:                 runner.LiveRunnerModePersistent,
		App:                  "live-video-to-video/scope",
		Capacity:             1,
		PriceInfo:            runner.LiveRunnerPriceInfo{Price: "0.001", Unit: "fixed"},
		SessionAdmissionPath: "/%2e%2e%2fescape",
	}, orch.RegistrationSecret())
	require.Error(t, err)
	registration, err := manager.Heartbeat(runner.LiveRunnerHeartbeatRequest{
		RunnerID:             "covered-runner",
		RunnerURL:            callback.URL + "/native",
		Status:               "ready",
		Mode:                 runner.LiveRunnerModePersistent,
		App:                  "live-video-to-video/scope",
		Capacity:             2,
		PriceInfo:            runner.LiveRunnerPriceInfo{Price: "0.001", Unit: "fixed"},
		SessionAdmissionPath: "/session-admission",
	}, orch.RegistrationSecret())
	require.NoError(t, err)

	capacityUsed := func() int {
		runners := manager.Runners()
		require.Len(t, runners, 1)
		return runners[0].CapacityUsed
	}
	request := func(token string, withPayment bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/apps/"+registration.RunnerID+"/session", nil)
		req.Header.Set(liveRunnerSenderHeader, orch.Address().Hex())
		if token != "" {
			req.Header.Set(runner.LiveRunnerSessionAdmissionHeader, token)
		}
		if withPayment {
			req.Header.Set(paymentHeader, "second-payment-forbidden")
		}
		lp.ServeHTTP(w, req)
		return w
	}

	first := request(admissionToken, false)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, 1, capacityUsed())
	require.Equal(t, 1, callbackCalls)
	require.Equal(t, admissionToken, callbackBody.Token)
	require.Equal(t, registration.RunnerID, callbackBody.RunnerID)
	require.Equal(t, orch.Address().Hex(), callbackBody.PayerAddress)
	require.Equal(t, "wei", callbackBody.Quote.Currency)
	require.Equal(t, "fixed", callbackBody.Quote.Unit)
	require.NotEmpty(t, callbackBody.Quote.Price)
	// Covered admission must not invoke the normal payment/accounting path.
	require.Empty(t, orch.balances)

	missing := request("", false)
	require.Equal(t, http.StatusForbidden, missing.Code)
	require.Equal(t, 1, capacityUsed())
	require.Equal(t, 1, callbackCalls)

	invalid := request("not-a-grant", false)
	require.Equal(t, http.StatusForbidden, invalid.Code)
	require.Equal(t, 1, capacityUsed())
	require.Equal(t, 1, callbackCalls)

	callbackAuthorized = false
	denied := request(strings.Repeat("cd", 24), false)
	require.Equal(t, http.StatusForbidden, denied.Code)
	require.Equal(t, 1, capacityUsed())
	require.Equal(t, 2, callbackCalls)

	callbackAuthorized = true
	replay := request(admissionToken, false)
	require.Equal(t, http.StatusForbidden, replay.Code)
	require.Equal(t, 1, capacityUsed())
	require.Equal(t, 3, callbackCalls)

	callbackStatus = http.StatusInternalServerError
	failed := request(strings.Repeat("ef", 24), false)
	require.Equal(t, http.StatusBadGateway, failed.Code)
	require.Equal(t, 1, capacityUsed())
	require.Equal(t, 4, callbackCalls)

	callbackStatus = http.StatusOK
	payment := request(strings.Repeat("12", 24), true)
	require.Equal(t, http.StatusBadRequest, payment.Code)
	require.Equal(t, 1, capacityUsed())
	require.Equal(t, 4, callbackCalls)
}

func TestLiveRunnerCoveredSessionAdmissionRejectsRegistrationChange(t *testing.T) {
	lp := newLiveRunnerHTTPOnchain(t)
	orch := lp.orchestrator.(*stubOrchestrator)
	manager := lp.node.LiveRunnerManager.(*runner.LiveRunnerRegistry)
	mutated := false
	heartbeatSecret := ""
	var callback *httptest.Server
	callback = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mutated {
			mutated = true
			_, err := manager.Heartbeat(runner.LiveRunnerHeartbeatRequest{
				RunnerID:             "covered-race",
				RunnerURL:            callback.URL + "/native",
				Status:               "ready",
				Mode:                 runner.LiveRunnerModePersistent,
				App:                  "live-video-to-video/scope",
				Capacity:             1,
				PriceInfo:            runner.LiveRunnerPriceInfo{Price: "0.001", Unit: "fixed"},
				SessionAdmissionPath: "/changed-admission",
			}, heartbeatSecret)
			if err != nil {
				t.Errorf("mutating runner registration: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorized":true}`))
	}))
	defer callback.Close()

	registration, err := manager.Heartbeat(runner.LiveRunnerHeartbeatRequest{
		RunnerID:             "covered-race",
		RunnerURL:            callback.URL + "/native",
		Status:               "ready",
		Mode:                 runner.LiveRunnerModePersistent,
		App:                  "live-video-to-video/scope",
		Capacity:             1,
		PriceInfo:            runner.LiveRunnerPriceInfo{Price: "0.001", Unit: "fixed"},
		SessionAdmissionPath: "/session-admission",
	}, orch.RegistrationSecret())
	require.NoError(t, err)
	heartbeatSecret = registration.HeartbeatSecret

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/apps/"+registration.RunnerID+"/session", nil)
	req.Header.Set(liveRunnerSenderHeader, orch.Address().Hex())
	req.Header.Set(runner.LiveRunnerSessionAdmissionHeader, strings.Repeat("ab", 24))
	lp.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	require.True(t, mutated)
	require.Len(t, manager.Runners(), 1)
	require.Equal(t, 0, manager.Runners()[0].CapacityUsed)
}
