// D-269 S2 测试：TronScan HTTP fetch（httptest.Server mock）。
package trxvote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchSRList_Normal 正常响应 → 解析为 []SRInfo。
func TestFetchSRList_Normal(t *testing.T) {
	fixture := `[{"address":"Tabc","name":"SR1","annualizedRate":"5.123","brokerage":0,"realTimeVotes":100000,"votes":100000,"producer":true,"producedTotal":100},{"address":"Tdef","name":"SR2","annualizedRate":"3.456","brokerage":10,"realTimeVotes":200000,"votes":200000,"producer":false,"producedTotal":0}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(&http.Client{})

	srs, err := fetchSRList(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchSRList: %v", err)
	}
	if len(srs) != 2 {
		t.Fatalf("len = %d, want 2", len(srs))
	}
	if srs[0].Address != "Tabc" {
		t.Errorf("srs[0].Address = %q, want Tabc", srs[0].Address)
	}
	if srs[0].AnnualizedRate < 5.1 || srs[0].AnnualizedRate > 5.2 {
		t.Errorf("srs[0].AnnualizedRate = %.4f, want ~5.123", srs[0].AnnualizedRate)
	}
	if srs[0].Brokerage != 0 {
		t.Errorf("srs[0].Brokerage = %d, want 0", srs[0].Brokerage)
	}
	if srs[0].Rank != 1 {
		t.Errorf("srs[0].Rank = %d, want 1", srs[0].Rank)
	}
	if srs[1].Rank != 2 {
		t.Errorf("srs[1].Rank = %d, want 2", srs[1].Rank)
	}
}

// TestFetchSRList_Empty 空响应 → 空列表。
func TestFetchSRList_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(&http.Client{})

	srs, err := fetchSRList(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchSRList: %v", err)
	}
	if len(srs) != 0 {
		t.Errorf("len = %d, want 0 (empty response)", len(srs))
	}
}

// TestFetchSRList_HTTPError HTTP 错误 → error。
func TestFetchSRList_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	SetHTTPClient(srv.Client())
	defer SetHTTPClient(&http.Client{})

	_, err := fetchSRList(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("fetchSRList should return error on HTTP 500")
	}
}
