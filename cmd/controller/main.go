package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Title string `json:"title"`
		Head  struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

func handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var event pullRequestEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	switch event.Action {
	case "opened", "reopened", "synchronize":
		log.Printf("PR #%d %s -> would create preview (sha %s)",
			event.Number, event.Action, event.PullRequest.Head.SHA)
	case "closed":
		log.Printf("PR #%d closed -> would destroy preview", event.Number)
	default:
		log.Printf("PR #%d: ignoring action %q", event.Number, event.Action)
	}

	w.WriteHeader(http.StatusAccepted)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/webhook", handleWebhook)

	log.Printf("launchpad-controller listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
