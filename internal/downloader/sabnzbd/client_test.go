package sabnzbd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "addurl" {
			t.Errorf("expected mode=addurl, got %s", r.URL.Query().Get("mode"))
		}
		if r.URL.Query().Get("cat") != "books" {
			t.Errorf("expected cat=books, got %s", r.URL.Query().Get("cat"))
		}
		json.NewEncoder(w).Encode(AddURLResponse{
			Status: true,
			NzoIDs: []string{"SABnzbd_nzo_abc123"},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", false)
	c.baseURL = srv.URL

	resp, err := c.AddURL(context.Background(), "https://example.com/nzb/123", "Test Book", "books", 0)
	if err != nil {
		t.Fatalf("add url: %v", err)
	}
	if !resp.Status {
		t.Error("expected status=true")
	}
	if len(resp.NzoIDs) != 1 || resp.NzoIDs[0] != "SABnzbd_nzo_abc123" {
		t.Errorf("unexpected nzo ids: %v", resp.NzoIDs)
	}
}

func TestGetQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(QueueResponse{
			Queue: QueueData{
				Status: "Downloading",
				Speed:  "5.2 M",
				Slots: []QueueSlot{
					{
						NzoID:      "SABnzbd_nzo_abc123",
						Filename:   "Test Book",
						Status:     "Downloading",
						Category:   "books",
						MB:         "100.0",
						MBLeft:     "50.0",
						Percentage: "50",
						TimeLeft:   "0:01:00",
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", false)
	c.baseURL = srv.URL

	queue, err := c.GetQueue(context.Background())
	if err != nil {
		t.Fatalf("get queue: %v", err)
	}
	if queue.Status != "Downloading" {
		t.Errorf("expected status Downloading, got %s", queue.Status)
	}
	if len(queue.Slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(queue.Slots))
	}
	if queue.Slots[0].Percentage != "50" {
		t.Errorf("expected 50%%, got %s", queue.Slots[0].Percentage)
	}
}

func TestGetHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HistoryResponse{
			History: HistoryData{
				TotalSize: "5 GB",
				Slots: []HistorySlot{
					{
						NzoID:    "SABnzbd_nzo_def456",
						Name:     "Completed Book",
						Status:   "Completed",
						Category: "books",
						Size:     "5.2 MB",
						Path:     "/downloads/complete/books/Completed Book",
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", false)
	c.baseURL = srv.URL

	history, err := c.GetHistory(context.Background(), "books", 20)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	if len(history.Slots) != 1 {
		t.Fatalf("expected 1 history slot, got %d", len(history.Slots))
	}
	if history.Slots[0].Status != "Completed" {
		t.Errorf("expected Completed, got %s", history.Slots[0].Status)
	}
}

func TestGetCategories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(CategoriesResponse{
			Categories: []string{"*", "books", "movies", "tv"},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", false)
	c.baseURL = srv.URL

	cats, err := c.GetCategories(context.Background())
	if err != nil {
		t.Fatalf("get categories: %v", err)
	}
	if len(cats) != 4 {
		t.Errorf("expected 4 categories, got %d", len(cats))
	}
}

func TestDeleteHistory(t *testing.T) {
	var gotMode, gotName, gotValue, gotDelFiles string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotMode = q.Get("mode")
		gotName = q.Get("name")
		gotValue = q.Get("value")
		gotDelFiles = q.Get("del_files")
		json.NewEncoder(w).Encode(SimpleResponse{Status: true})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", false)
	c.baseURL = srv.URL

	if err := c.DeleteHistory(context.Background(), "SABnzbd_nzo_def456", false); err != nil {
		t.Fatalf("delete history: %v", err)
	}
	if gotMode != "history" || gotName != "delete" || gotValue != "SABnzbd_nzo_def456" {
		t.Errorf("unexpected params: mode=%s name=%s value=%s", gotMode, gotName, gotValue)
	}
	if gotDelFiles != "" {
		t.Errorf("del_files should be unset when deleteFiles=false, got %q", gotDelFiles)
	}

	if err := c.DeleteHistory(context.Background(), "nzo_xyz", true); err != nil {
		t.Fatalf("delete history with files: %v", err)
	}
	if gotDelFiles != "1" {
		t.Errorf("del_files should be 1 when deleteFiles=true, got %q", gotDelFiles)
	}
}

func TestTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(CategoriesResponse{
			Categories: []string{"*", "books"},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", false)
	c.baseURL = srv.URL

	err := c.Test(context.Background())
	if err != nil {
		t.Errorf("test should pass: %v", err)
	}
}
