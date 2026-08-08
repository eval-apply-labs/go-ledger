// Command go_ledger_ui is a friendly local wallet for a go_ledger network. It holds the user's
// key locally, shows their balance and the accounts on the ledger, and lets them send a payment
// with a simple form -- instead of hand-editing JSON and signatures, as the original required.
//
// Usage:
//
//	go_ledger_ui -node http://127.0.0.1:8080 -network village -wallet wallet.json -listen 127.0.0.1:8090
//
// then open http://127.0.0.1:8090 in a browser.
package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
)

//go:embed templates/*.html
var tmplFS embed.FS

var tmpl = template.Must(template.ParseFS(tmplFS, "templates/*.html"))

func main() {
	node := flag.String("node", "http://127.0.0.1:8080", "node base URL")
	listen := flag.String("listen", "127.0.0.1:8090", "address to serve the wallet UI on")
	network := flag.String("network", "village", "network id (must match the node)")
	keyfile := flag.String("wallet", "wallet.json", "wallet key file (created if absent)")
	lookahead := flag.Int64("lookahead", 3, "blocks ahead a payment names (raise if payments miss their block)")
	flag.Parse()

	w, err := LoadOrCreateWallet(*keyfile, *network, *node, *lookahead)
	if err != nil {
		log.Fatalf("wallet: %v", err)
	}

	h := &handler{w: w}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.index)
	mux.HandleFunc("/admin", h.admin)
	// Mutating endpoints auto-sign with the local key, so they are guarded against cross-site
	// (CSRF / drive-by) requests — a page the user merely visits must not be able to submit a
	// payment or a governance vote.
	mux.HandleFunc("/send", guard(h.send))
	mux.HandleFunc("/register", guard(h.register))
	mux.HandleFunc("/admin/parameter", guard(h.proposeParameter))
	mux.HandleFunc("/admin/reassignment", guard(h.voteReassignment))

	log.Printf("go_ledger wallet on http://%s  (account %s…, node %s)", *listen, first(w.PublicKey(), 12), *node)
	log.Fatal(http.ListenAndServe(*listen, mux))
}

type handler struct{ w *Wallet }

// guard blocks cross-site POSTs to a mutating endpoint. The wallet listens on localhost and
// auto-signs with the local key, so without this any web page the user visits while the wallet runs
// could silently trigger a signed payment or governance vote (CSRF / drive-by).
func guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "cross-site request blocked", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// sameOrigin reports whether a request is same-origin. It trusts the browser's Sec-Fetch-Site label
// (a cross-site form post is marked "cross-site") and, if an Origin header is present, requires it to
// match the served host. A request with neither header (a non-browser client like curl, or a
// top-level form navigation) is allowed — the local operator, not a foreign page.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	}
	return true
}

type pageData struct {
	PublicKey string
	Name      string // this wallet's registered name, if any
	Balance   string
	Height    int64
	Accounts  []acctRow
	Message   string
	Error     string
}

type acctRow struct{ Name, PublicKey, Amount string }

func (h *handler) index(wr http.ResponseWriter, r *http.Request) {
	h.render(wr, r.URL.Query().Get("msg"), r.URL.Query().Get("err"))
}

func (h *handler) send(wr http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(wr, r, "/", http.StatusSeeOther)
		return
	}
	receipt, verified, err := h.w.Send(r.FormValue("to"), r.FormValue("amount"))
	if err != nil {
		http.Redirect(wr, r, "/?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	msg := "payment submitted"
	if verified {
		// Surface the node's signed receipt — provisional proof a VALIDATOR accepted and queued THIS
		// payment (signature valid, PaymentID matches, node is in the validator set).
		msg = fmt.Sprintf("payment queued for block %d — receipt from validator %s… verified ✓", receipt.BlockID, first(receipt.Node, 12))
	}
	http.Redirect(wr, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (h *handler) register(wr http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(wr, r, "/", http.StatusSeeOther)
		return
	}
	if err := h.w.RegisterName(r.FormValue("name"), r.FormValue("description")); err != nil {
		http.Redirect(wr, r, "/?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(wr, r, "/?msg="+url.QueryEscape("name registration submitted — it appears once its block commits"), http.StatusSeeOther)
}

func (h *handler) render(wr http.ResponseWriter, msg, errMsg string) {
	data := pageData{PublicKey: h.w.PublicKey(), Message: msg, Error: errMsg}
	if bal, err := h.w.Balance(); err == nil {
		data.Balance = bal.FloatString(2)
	} else if data.Error == "" {
		data.Error = "cannot reach node: " + err.Error()
	}
	data.Height = h.w.height()
	if accts, err := h.w.accounts(); err == nil {
		sort.Slice(accts, func(i, j int) bool { return accts[i].Name < accts[j].Name })
		for _, a := range accts {
			amt := "0"
			if a.Amount != nil {
				amt = a.Amount.FloatString(2)
			}
			if a.PublicKey == data.PublicKey {
				data.Name = a.Name
			}
			data.Accounts = append(data.Accounts, acctRow{Name: a.Name, PublicKey: a.PublicKey, Amount: amt})
		}
	}
	if err := tmpl.ExecuteTemplate(wr, "wallet.html", data); err != nil {
		log.Printf("render: %v", err)
	}
}

type adminPageData struct {
	PublicKey          string
	IsValidator        bool
	TransactionCost    string
	BlockIntervalMs    int64
	StepTimeoutMs      int64
	MaxPeers           int
	MaxMessageBytes    int
	TotalPower         int64
	Quorum             int64
	ParameterProposals []parameterProposalView
	Reassignments      []reassignmentView
	Message            string
	Error              string
}

func (h *handler) admin(wr http.ResponseWriter, r *http.Request) {
	data := adminPageData{
		PublicKey: h.w.PublicKey(),
		Message:   r.URL.Query().Get("msg"),
		Error:     r.URL.Query().Get("err"),
	}
	if view, err := h.w.Governance(); err != nil {
		if data.Error == "" {
			data.Error = "cannot reach node: " + err.Error()
		}
	} else {
		data.IsValidator = h.w.IsValidator(view)
		data.TransactionCost = view.TransactionCost
		data.BlockIntervalMs = view.BlockIntervalMs
		data.StepTimeoutMs = view.StepTimeoutMs
		data.MaxPeers = view.MaxPeers
		data.MaxMessageBytes = view.MaxMessageBytes
		data.TotalPower = view.TotalPower
		data.Quorum = view.Quorum
		data.ParameterProposals = view.ParameterProposals
		data.Reassignments = view.Reassignments
	}
	if err := tmpl.ExecuteTemplate(wr, "admin.html", data); err != nil {
		log.Printf("admin render: %v", err)
	}
}

func (h *handler) proposeParameter(wr http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(wr, r, "/admin", http.StatusSeeOther)
		return
	}
	err := h.w.ProposeParameters(
		r.FormValue("cost"), r.FormValue("blockIntervalMs"), r.FormValue("stepTimeoutMs"),
		r.FormValue("maxPeers"), r.FormValue("maxMessageBytes"))
	if err != nil {
		http.Redirect(wr, r, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(wr, r, "/admin?msg="+url.QueryEscape("parameter vote submitted — it enacts once ⅔ of validators agree"), http.StatusSeeOther)
}

func (h *handler) voteReassignment(wr http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(wr, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := h.w.VoteReassignment(r.FormValue("account"), r.FormValue("amount")); err != nil {
		http.Redirect(wr, r, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(wr, r, "/admin?msg="+url.QueryEscape("reassignment vote submitted — it enacts once ⅔ of validators agree"), http.StatusSeeOther)
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
