package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"
)

var commit = "dev"

type view struct {
	PR         string
	Commit     string
	FullCommit string
	Namespace  string
	Pod        string
	TTL        string
	Started    string
	Expires    string
	Remaining  string
	Percent    int
	Expired    bool
}

func short(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// humanDuration renders a duration the way a person reads a countdown:
// "2d 7h" when there is plenty left, "14m" when there is not.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 1))
	}
}

type environment struct {
	pr, namespace, pod string
	ttl                time.Duration
	ttlRaw             string
	started            time.Time
}

func loadEnvironment(getenv func(string) string, started time.Time) environment {
	val := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}

	raw := val("TTL", "72h")
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		ttl = 72 * time.Hour
		raw = "72h"
	}

	return environment{
		pr:        val("PR_NUMBER", "?"),
		namespace: val("NAMESPACE", "unknown"),
		pod:       val("POD_NAME", "unknown"),
		ttl:       ttl,
		ttlRaw:    raw,
		started:   started,
	}
}

// render is recomputed per request, so the countdown is correct without any
// client-side script.
func (e environment) render(now time.Time) view {
	expires := e.started.Add(e.ttl)
	left := expires.Sub(now)

	percent := 0
	if e.ttl > 0 {
		percent = int(float64(now.Sub(e.started)) / float64(e.ttl) * 100)
	}
	percent = min(max(percent, 0), 100)

	return view{
		PR:         e.pr,
		Commit:     short(commit),
		FullCommit: commit,
		Namespace:  e.namespace,
		Pod:        e.pod,
		TTL:        e.ttlRaw,
		Started:    e.started.UTC().Format("2 Jan 2006, 15:04 UTC"),
		Expires:    expires.UTC().Format("2 Jan 2006, 15:04 UTC"),
		Remaining:  humanDuration(left),
		Percent:    percent,
		Expired:    left <= 0,
	}
}

var page = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PR #{{.PR}} · Launchpad preview</title>
<style>
  :root {
    color-scheme: dark light;
    --bg: #080b14;
    --glow: #1d4ed8;
    --surface: rgba(255, 255, 255, .04);
    --border: rgba(255, 255, 255, .09);
    --border-strong: rgba(255, 255, 255, .14);
    --text: #f1f5f9;
    --muted: #8b97ad;
    --accent: #5eead4;
    --live: #34d399;
    --track: rgba(255, 255, 255, .08);
  }

  @media (prefers-color-scheme: light) {
    :root {
      --bg: #f5f7fb;
      --glow: #93c5fd;
      --surface: #ffffff;
      --border: rgba(15, 23, 42, .09);
      --border-strong: rgba(15, 23, 42, .16);
      --text: #0f172a;
      --muted: #5b6678;
      --accent: #0d9488;
      --live: #059669;
      --track: rgba(15, 23, 42, .08);
    }
  }

  * { box-sizing: border-box; }

  body {
    margin: 0;
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 2.5rem 1rem;
    background: var(--bg);
    background-image:
      radial-gradient(60rem 30rem at 50% -12rem, color-mix(in srgb, var(--glow) 28%, transparent), transparent 70%);
    color: var(--text);
    font: 15px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
    -webkit-font-smoothing: antialiased;
  }

  .card {
    width: min(34rem, 100%);
    background: var(--surface);
    backdrop-filter: blur(12px);
    border: 1px solid var(--border);
    border-radius: 18px;
    box-shadow: 0 1px 0 rgba(255, 255, 255, .05) inset, 0 24px 60px -24px rgba(0, 0, 0, .6);
    overflow: hidden;
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 1rem 1.5rem;
    border-bottom: 1px solid var(--border);
  }

  .brand { display: flex; align-items: center; gap: .6rem; font-weight: 600; letter-spacing: -.01em; }
  .brand svg { display: block; }

  .status {
    display: inline-flex;
    align-items: center;
    gap: .45rem;
    padding: .3rem .7rem .3rem .55rem;
    border: 1px solid var(--border-strong);
    border-radius: 999px;
    font-size: .75rem;
    font-weight: 600;
    letter-spacing: .04em;
    text-transform: uppercase;
    color: var(--live);
  }

  .dot {
    width: .45rem; height: .45rem; border-radius: 50%;
    background: currentColor;
    box-shadow: 0 0 0 0 currentColor;
    animation: pulse 2.4s ease-out infinite;
  }
  @keyframes pulse {
    0%   { box-shadow: 0 0 0 0 color-mix(in srgb, currentColor 70%, transparent); }
    70%  { box-shadow: 0 0 0 .5rem transparent; }
    100% { box-shadow: 0 0 0 0 transparent; }
  }
  @media (prefers-reduced-motion: reduce) { .dot { animation: none; } }

  .body { padding: 1.75rem 1.5rem 1.5rem; }

  h1 {
    margin: 0;
    font-size: clamp(1.6rem, 1.2rem + 2vw, 2.1rem);
    font-weight: 650;
    letter-spacing: -.025em;
  }
  h1 .hash { color: var(--muted); font-weight: 500; }

  .lede { margin: .4rem 0 1.6rem; color: var(--muted); }

  dl {
    margin: 0;
    display: grid;
    grid-template-columns: 7.5rem 1fr;
    gap: 0;
  }
  dt, dd {
    margin: 0;
    padding: .7rem 0;
    border-top: 1px solid var(--border);
  }
  dt { color: var(--muted); font-size: .875rem; }
  dd {
    font-family: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, monospace;
    font-size: .875rem;
    overflow-wrap: anywhere;
  }
  dd .accent { color: var(--accent); }

  .ttl { margin-top: 1.5rem; }
  .ttl-row { display: flex; align-items: baseline; justify-content: space-between; gap: 1rem; margin-bottom: .55rem; }
  .ttl-label { color: var(--muted); font-size: .875rem; }
  .ttl-value { font-size: .875rem; font-weight: 600; font-variant-numeric: tabular-nums; }
  .track { height: 5px; border-radius: 999px; background: var(--track); overflow: hidden; }
  .fill {
    height: 100%;
    min-width: .75rem;
    border-radius: 999px;
    background: linear-gradient(90deg, var(--accent), color-mix(in srgb, var(--accent) 35%, var(--glow)));
  }
  .fill.late { background: linear-gradient(90deg, #fbbf24, #f87171); }

  footer {
    padding: .95rem 1.5rem;
    border-top: 1px solid var(--border);
    color: var(--muted);
    font-size: .8rem;
  }

  @media (max-width: 26rem) {
    dl { grid-template-columns: 1fr; }
    dd { padding-top: 0; border-top: 0; }
    dt { padding-bottom: .2rem; }
  }
</style>
</head>
<body>
<main class="card">
  <header>
    <span class="brand">
      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="M5 15c-1.5 1.5-2 5-2 5s3.5-.5 5-2c.9-.9.9-2.2 0-3a2.1 2.1 0 0 0-3 0z"/>
        <path d="M17.8 10.2 13.8 14.2l-4-4 4-4a7.5 7.5 0 0 1 6.6-2.6 7.5 7.5 0 0 1-2.6 6.6z"/>
        <path d="M13.8 14.2 11 17m-1-6.8L7.2 13"/>
      </svg>
      Launchpad
    </span>
    <span class="status"><span class="dot"></span>{{if .Expired}}Expiring{{else}}Live{{end}}</span>
  </header>

  <div class="body">
    <h1><span class="hash">Pull request</span> #{{.PR}}</h1>
    <p class="lede">A disposable Kubernetes environment, built for this pull request alone.</p>

    <dl>
      <dt>Commit</dt><dd title="{{.FullCommit}}"><span class="accent">{{.Commit}}</span></dd>
      <dt>Namespace</dt><dd>{{.Namespace}}</dd>
      <dt>Pod</dt><dd>{{.Pod}}</dd>
      <dt>Created</dt><dd>{{.Started}}</dd>
    </dl>

    <div class="ttl">
      <div class="ttl-row">
        <span class="ttl-label">Time remaining</span>
        <span class="ttl-value">{{.Remaining}} of {{.TTL}}</span>
      </div>
      <div class="track" role="img" aria-label="{{.Percent}} percent of this environment's lifetime has elapsed">
        <div class="fill{{if gt .Percent 75}} late{{end}}" style="width: {{.Percent}}%"></div>
      </div>
    </div>
  </div>

  <footer>Destroyed when the pull request closes, or at {{.Expires}}.</footer>
</main>
</body>
</html>
`))

func newHandler(env environment, now func() time.Time) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:")
		if err := page.Execute(w, env.render(now())); err != nil {
			log.Printf("render: %v", err)
		}
	})

	return mux
}

func main() {
	env := loadEnvironment(os.Getenv, time.Now())

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(env, time.Now),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("launchpad demo for PR #%s (commit %s) listening on :%s", env.pr, short(commit), port)
	log.Fatal(srv.ListenAndServe())
}
