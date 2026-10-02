// Command testserver serves the fixture pages that test.sh drives rodney
// against, on 127.0.0.1:18080. `make test-sh` builds it and runs test.sh.
package main

import (
	"html"
	"log"
	"net/http"
)

const indexHTML = `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>Hello Rod CLI</h1>
<a id="link1" href="/page2">Page 2</a>
<ul>
<li class="item">One</li>
<li class="item">Two</li>
<li class="item">Three</li>
</ul>
<button id="btn1" onclick="document.getElementById('info').textContent = 'Clicked!'">Click me</button>
<p id="info">Not clicked</p>
<p id="hidden" style="display: none">Hidden</p>
<input id="search" type="text">
<select id="color">
<option value="red">Red</option>
<option value="green">Green</option>
<option value="blue">Blue</option>
</select>
</body>
</html>
`

const page2HTML = `<!DOCTYPE html>
<html>
<head><title>Page 2</title></head>
<body><h1>Page 2</h1><a href="/">Back</a></body>
</html>
`

func page(html string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}
}

// hints echoes the User-Agent client hint the request carried, so test.sh can
// check the site provisions (quirks.go) on the very first request.
func hints(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Hints</title></head><body><pre id="ch">` +
		html.EscapeString(r.Header.Get("Sec-CH-UA")) + `</pre></body></html>`))
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", page(indexHTML))
	mux.HandleFunc("GET /page2", page(page2HTML))
	mux.HandleFunc("GET /hints", hints)
	log.Fatal(http.ListenAndServe("127.0.0.1:18080", mux))
}
