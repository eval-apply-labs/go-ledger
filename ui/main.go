// Command go_ledger_ui is a friendly local wallet for a go_ledger network. It holds the user's
// key locally, shows their name, their balance and the accounts on the ledger, and lets them pay by
// name, freeze their name, and draft and sign name requests (a bank's desk included) and governance
// votes with simple forms -- instead of hand-editing JSON and signatures, as the original required.
//
// Usage:
//
//	go_ledger_ui -node http://127.0.0.1:8080 -network village -wallet wallet.json -listen 127.0.0.1:8090
//
// then open http://127.0.0.1:8090 in a browser. A bank's wallet whose key is in the bank's signer
// (cmd/signer) holds no key: it signs through the signer, and its wallet file holds the bank's public
// key only. A wallet that listens beyond loopback asks for a password, the one in -password-file.
//
//	go_ledger_ui -node http://127.0.0.1:8080 -network village -wallet bank.json -listen :8090 \
//	  -password-file wallet.password \
//	  -signer-url http://172.28.71.254:7000 -signer-client desk -signer-secret-file desk.secret
package main

import (
	"crypto/subtle"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
)

//go:embed templates/*.html
var templateFiles embed.FS

var pageTemplates = template.Must(template.New("pages").Funcs(template.FuncMap{"fingerprint": fingerprint}).ParseFS(templateFiles, "templates/*.html"))

func main() {
	nodeURL := flag.String("node", "http://127.0.0.1:8080", "node base URL: a bank's public address, or, for the bank's operator, its engine's operator port")
	listen := flag.String("listen", "127.0.0.1:8090", "address to serve the wallet UI on")
	network := flag.String("network", "village", "network name (the node's network id must start with it and a slash)")
	keyfile := flag.String("wallet", "wallet.json", "wallet key file (created if absent)")
	lookahead := flag.Int64("lookahead", 3, fmt.Sprintf("blocks ahead a payment or a freeze names, at most %d (raise if they miss their block)", node.MaxBlockIDLookahead-blockSlack))
	// A bank's wallet whose key is in the signer program (spec §2.7) names it, and -wallet is then a file
	// holding the bank's public key only.
	signerURL := flag.String("signer-url", "", "a bank's wallet: the signer that holds the bank's key, such as http://172.28.71.254:7000; -wallet then holds the bank's public key only")
	signerClient := flag.String("signer-client", "", "with -signer-url: the client name the signer's config gives this wallet, such as desk or admin")
	signerSecretFile := flag.String("signer-secret-file", "", "with -signer-url: the file holding that client's secret, as keygen -secret prints it")
	passwordFile := flag.String("password-file", "", "a file holding the password the wallet asks for, under any user name; required when -listen is not a loopback address")
	flag.Parse()

	// Before the wallet file: a wallet that would refuse to start must not make a key first.
	password, err := walletPassword(*listen, *passwordFile)
	if err != nil {
		log.Fatalf("wallet: %v", err)
	}
	wallet, err := loadWallet(*keyfile, *network, *nodeURL, *lookahead, signerSettings{url: *signerURL, client: *signerClient, secretFile: *signerSecretFile})
	if err != nil {
		log.Fatalf("wallet: %v", err)
	}

	log.Printf("go_ledger wallet on http://%s  (key %s…, node %s)", *listen, first(wallet.PublicKey(), 12), *nodeURL)
	log.Fatal(http.ListenAndServe(*listen, routes(wallet, *listen, password)))
}

// walletPassword is the password the wallet asks for: the one in passwordFile, or none when there is
// no file. A wallet that listens beyond loopback must have one: guard stops a browser's page, never a
// program on the network, which sends whatever headers it likes, and the wallet signs for its key.
func walletPassword(listen, passwordFile string) (string, error) {
	if passwordFile == "" {
		if !loopbackOnly(listen) {
			return "", fmt.Errorf("-listen %s reaches beyond this machine: give the wallet a password with -password-file", listen)
		}
		return "", nil
	}
	data, err := os.ReadFile(passwordFile)
	if err != nil {
		return "", err
	}
	password := strings.TrimSpace(string(data))
	if password == "" {
		return "", fmt.Errorf("%s holds no password", passwordFile)
	}
	return password, nil
}

// loopbackOnly reports whether listen (a -listen address) is on loopback only: a loopback IP address.
// An empty host, 0.0.0.0, any other address, and any host name can be reached from elsewhere, even
// localhost, which the system's hosts file or DNS resolves.
func loopbackOnly(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	hostIP := net.ParseIP(host)
	return hostIP != nil && hostIP.IsLoopback()
}

// requirePassword lets through only a request carrying password as HTTP Basic auth, under any user
// name. The browser asks for it once, and sends it with every request to the wallet after that.
func requirePassword(password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_, given, _ := request.BasicAuth() // none gives "", never the password, which is not empty
		if subtle.ConstantTimeCompare([]byte(given), []byte(password)) != 1 {
			responseWriter.Header().Set("WWW-Authenticate", `Basic realm="go_ledger wallet", charset="UTF-8"`)
			http.Error(responseWriter, "this wallet asks for its password", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(responseWriter, request)
	})
}

type handler struct{ wallet *Wallet }

// routes is the wallet's HTTP surface. Every route sits behind guard: the mutating ones auto-sign
// with the local key, so a page the user merely visits must not be able to submit a payment, a freeze,
// a name request or a governance vote (CSRF / drive-by / DNS rebinding). Behind guard, a password, when
// there is one, stops a program that sends the headers guard wants. guard comes first, so a page of
// another site gets guard's refusal, never a login box under its own host name.
func routes(wallet *Wallet, listen, password string) http.Handler {
	walletHandler := &handler{wallet: wallet}
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/", walletHandler.index)
	serveMux.HandleFunc("/send", walletHandler.send)
	serveMux.HandleFunc("/update", walletHandler.updateAccount)
	serveMux.HandleFunc("/freeze", walletHandler.freeze)
	serveMux.HandleFunc("/checkpoint", walletHandler.loadCheckpoint)
	serveMux.HandleFunc("/requests", walletHandler.requests)
	serveMux.HandleFunc("/admin", walletHandler.admin)
	serveMux.HandleFunc("/admin/parameter", walletHandler.proposeParameter)
	serveMux.HandleFunc("/admin/reassignment", walletHandler.voteReassignment)
	if password == "" {
		return guard(listen, serveMux)
	}
	return guard(listen, requirePassword(password, serveMux))
}

// guard refuses any request sameOrigin does not accept, and forbids framing the wallet's pages, so
// another site cannot overlay its buttons either.
func guard(listen string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("X-Frame-Options", "DENY")
		responseWriter.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		if !sameOrigin(request, listen) {
			http.Error(responseWriter, "cross-site request blocked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(responseWriter, request)
	})
}

// sameOrigin reports whether a request comes from the wallet's own pages. Its Host must be one of the
// wallet's own authorities (ownHost), whatever the Origin says: a DNS-rebinding page reaches the
// wallet under the attacker's host name, with a matching Origin. A read needs nothing more. Anything
// else must not be labelled cross-site by the browser (Sec-Fetch-Site), and must carry an Origin (or,
// failing that, a Referer) naming the same host: browsers send Origin on every POST, so a request
// with neither is not the operator's browser.
func sameOrigin(request *http.Request, listen string) bool {
	if !ownHost(request.Host, listen) {
		return false
	}
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	switch request.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	source := request.Header.Get("Origin")
	if source == "" {
		source = request.Header.Get("Referer")
	}
	sourceURL, err := url.Parse(source)
	return source != "" && err == nil && sourceURL.Host == request.Host
}

// ownHost reports whether host (a Host header) names the wallet on its -listen port: the listen host
// itself, localhost or a loopback address, or, when listening on every interface, any IP address
// (the operator then reaches it by address). Any other host name is refused: it is how a
// DNS-rebinding page addresses the wallet, and a page can never rebind an IP address.
func ownHost(host, listen string) bool {
	listenHost, port, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	name, requestPort, err := net.SplitHostPort(host)
	if err != nil || requestPort != port {
		return false
	}
	if name == listenHost || name == "localhost" {
		return true
	}
	hostIP := net.ParseIP(name)
	wildcard := listenHost == "" || net.ParseIP(listenHost).IsUnspecified()
	return hostIP != nil && (hostIP.IsLoopback() || wildcard)
}

// pageData is the wallet's main page. Every page reads one verified state, and shows and acts on it.
type pageData struct {
	PublicKey     string // the wallet's key in full: render finds the wallet's name by it, and the copy button copies it
	Fingerprint   string // the first 16 hex digits of the key's SHA-256 (fingerprint)
	Name          string // the name whose current key is this wallet's key, if any (spec 7.2)
	Becomes       string // else the name whose pending key change is to this wallet's key, if any
	BecomesAt     int64  // and the block at whose end that change takes effect
	LastName      string // the last name this wallet held, when its key holds none now
	FreezeName    string // the name the freeze button freezes: Name, or LastName when no state verifies
	Frozen        bool   // Name is frozen
	PendingChange string // a pending change on Name (pendingChange)
	PendingAt     int64  // and the block at whose end it takes effect
	FreezeStatus  string // what became of the freeze this wallet sent (Wallet.freezeStatus)
	Balance       string
	Verified      bool      // a state verified: the page shows it, and only then says "not registered"
	Height        int64     // the height of the verified state the page shows
	KeptFrom      string    // "" when that state is the node's; else "file" or "node", where the newer state that stands in came from (Wallet.newestVerifiedState)
	Banks         bankCheck // what the other banks' nodes say about that state (checkOtherBanks)
	Accounts      []accountRow
	Review        *paymentReview // a payment to confirm before it is signed
	Saved         *savedPayment  // a payment signed and saved as text, to carry to a bank (Wallet.SavePayment)
	ConfirmFreeze bool           // the freeze button was pressed: confirm before it signs
	Message       string
	Error         string
}

// accountRow is one account in the page's table: never its key in full, only its fingerprint.
type accountRow struct {
	Name, Bank, Fingerprint, Amount string
	Frozen                          bool
}

// index is the main page. The "/" pattern matches every path no other route takes, so any other path,
// /favicon.ico included, is not found, and reads no checkpoint.
func (walletHandler *handler) index(responseWriter http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(responseWriter, request)
		return
	}
	checkpoint, keptFrom, err := walletHandler.wallet.newestVerifiedState()
	walletHandler.render(responseWriter, request, pageData{KeptFrom: keptFrom, Message: request.URL.Query().Get("msg"), Error: request.URL.Query().Get("err")}, checkpoint, err)
}

// send reviews a payment (the default step): the page shows the payee's name and bank of record from the
// verified state, and signs nothing. Its "confirm" step signs the payment and submits it. Its "save" step
// signs the payment, sending nothing (spec §7.8), and redirects to GET /send, which shows the payment
// saved last as text: post, redirect, get, so a reload of that page signs no second payment.
func (walletHandler *handler) send(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		saved := walletHandler.wallet.lastSavedPayment()
		if saved == nil {
			http.Redirect(responseWriter, request, "/", http.StatusSeeOther)
			return
		}
		checkpoint, keptFrom, err := walletHandler.wallet.newestVerifiedState()
		walletHandler.render(responseWriter, request, pageData{Saved: saved, KeptFrom: keptFrom}, checkpoint, err)
		return
	}
	if step := request.FormValue("step"); step != "confirm" {
		checkpoint, keptFrom, err := walletHandler.wallet.newestVerifiedState()
		data := pageData{KeptFrom: keptFrom}
		if err == nil && step == "save" {
			if _, saveErr := walletHandler.wallet.SavePayment(checkpoint, request.FormValue("to"), request.FormValue("amount")); saveErr != nil {
				http.Redirect(responseWriter, request, "/?err="+url.QueryEscape(saveErr.Error()), http.StatusSeeOther)
				return
			}
			http.Redirect(responseWriter, request, "/send", http.StatusSeeOther)
			return
		} else if err == nil {
			review, reviewErr := walletHandler.wallet.ReviewPayment(checkpoint, request.FormValue("to"), request.FormValue("amount"))
			if reviewErr != nil {
				http.Redirect(responseWriter, request, "/?err="+url.QueryEscape(reviewErr.Error()), http.StatusSeeOther)
				return
			}
			data.Review = &review
		}
		walletHandler.render(responseWriter, request, data, checkpoint, err)
		return
	}
	receipt, verified, err := walletHandler.wallet.Send(request.FormValue("to"), request.FormValue("amount"))
	if err != nil {
		http.Redirect(responseWriter, request, "/?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	message := "payment submitted"
	if verified {
		// Surface the node's signed receipt — provisional proof a VALIDATOR accepted and queued THIS
		// payment (it names a pinned validator, verifies under its pinned key, and its PaymentID matches).
		message = fmt.Sprintf("payment queued for block %d — receipt from validator %s verified ✓", receipt.BlockID, receipt.Node)
	}
	http.Redirect(responseWriter, request, "/?msg="+url.QueryEscape(message), http.StatusSeeOther)
}

func (walletHandler *handler) updateAccount(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Redirect(responseWriter, request, "/", http.StatusSeeOther)
		return
	}
	update, err := walletHandler.wallet.UpdateAccount(request.FormValue("description"), request.FormValue("delegate"), request.FormValue("publicAddress"),
		request.FormValue("ipAddresses"), request.FormValue("tlsKeys"))
	if err != nil {
		http.Redirect(responseWriter, request, "/?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	message := "account update submitted — it appears once its block commits"
	if update.Delegate != "" && update.Delegate != ledger.ClearField {
		message = "account update submitted, delegating to " + update.Delegate + " — it appears once its block commits" // the name as the wallet read it
	}
	http.Redirect(responseWriter, request, "/?msg="+url.QueryEscape(message), http.StatusSeeOther)
}

// freeze asks to confirm (the default step), and its "confirm" step signs a freeze of the wallet's own
// name and submits it (spec 7.3).
func (walletHandler *handler) freeze(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Redirect(responseWriter, request, "/", http.StatusSeeOther)
		return
	}
	if request.FormValue("step") != "confirm" {
		checkpoint, keptFrom, err := walletHandler.wallet.newestVerifiedState()
		walletHandler.render(responseWriter, request, pageData{ConfirmFreeze: true, KeptFrom: keptFrom}, checkpoint, err)
		return
	}
	freeze, err := walletHandler.wallet.Freeze()
	if err != nil {
		http.Redirect(responseWriter, request, "/?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	message := fmt.Sprintf("freeze of %s sent for block %d", freeze.Name, freeze.BlockID)
	http.Redirect(responseWriter, request, "/?msg="+url.QueryEscape(message), http.StatusSeeOther)
}

// loadCheckpoint takes a checkpoint file someone carried in, pasted into the main page's form when the
// node can't be reached (Wallet.LoadCarriedCheckpoint, spec §7.8). The form is url-encoded, so it can be
// up to three times the size of the file, which may be as large as a node takes (node.MaxCheckpointBytes).
// Its body is bounded at that, which also lifts the 10 MB that Go's form parser reads of a body it is not
// given a bound for.
func (walletHandler *handler) loadCheckpoint(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Redirect(responseWriter, request, "/", http.StatusSeeOther)
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, 3*node.MaxCheckpointBytes)
	if err := request.ParseForm(); err != nil {
		reason := fmt.Sprintf("the form is unreadable (%v)", err)
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			reason = errCheckpointTooLarge.Error()
		}
		http.Redirect(responseWriter, request, "/?err="+url.QueryEscape("the checkpoint file is refused: "+reason), http.StatusSeeOther)
		return
	}
	checkpoint, err := walletHandler.wallet.LoadCarriedCheckpoint(request.FormValue("checkpoint"))
	if err != nil {
		http.Redirect(responseWriter, request, "/?err="+url.QueryEscape("the checkpoint file is refused: "+err.Error()), http.StatusSeeOther)
		return
	}
	message := fmt.Sprintf("checkpoint loaded: while your bank's node can't be reached, or serves an older state, the page shows the state as of height %d, from a file", checkpoint.State.Height)
	http.Redirect(responseWriter, request, "/?msg="+url.QueryEscape(message), http.StatusSeeOther)
}

// render shows the main page from one verified state, with what the other banks say of it
// (checkOtherBanks, asked within the page's request), or, when none verifies (readErr), what the wallet
// still knows: its key, its last name, and the freeze button for that name.
func (walletHandler *handler) render(responseWriter http.ResponseWriter, request *http.Request, data pageData, checkpoint node.Checkpoint, readErr error) {
	wallet := walletHandler.wallet
	data.PublicKey, data.Fingerprint = wallet.PublicKey(), fingerprint(wallet.PublicKey())
	if readErr != nil {
		if data.Error == "" {
			data.Error = "cannot read a verified state from the node: " + readErr.Error()
		}
		data.FreezeName, data.FreezeStatus = wallet.lastName(), wallet.freezeStatus(nil)
		walletHandler.execute(responseWriter, "wallet.html", data)
		return
	}
	state := checkpoint.State
	data.Verified, data.Height, data.FreezeStatus = true, state.Height, wallet.freezeStatus(&state)
	data.Banks = wallet.checkOtherBanks(request.Context(), checkpoint)
	for _, account := range state.Accounts {
		data.Accounts = append(data.Accounts, accountRow{Name: account.Name, Bank: account.Bank, Fingerprint: fingerprint(account.PublicKey),
			Amount: ledger.FormatAmount(account.Amount), Frozen: account.Frozen})
		switch {
		case account.PublicKey == data.PublicKey:
			data.Name, data.FreezeName, data.Frozen = account.Name, account.Name, account.Frozen
			data.Balance, data.PendingChange, data.PendingAt = ledger.FormatAmount(account.Amount), pendingChange(account), account.PendingHeight
		case account.PendingKey == data.PublicKey:
			data.Becomes, data.BecomesAt = account.Name, account.PendingHeight
		}
	}
	if data.Name == "" {
		data.LastName = wallet.lastName() // its key holds no name now: the page says that name has another key
	}
	walletHandler.execute(responseWriter, "wallet.html", data)
}

// execute writes the page from its template.
func (walletHandler *handler) execute(responseWriter http.ResponseWriter, page string, data any) {
	if err := pageTemplates.ExecuteTemplate(responseWriter, page, data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

// requestsPageData is the Requests page (spec 7.3), which is also a bank's desk (spec 7.4).
type requestsPageData struct {
	PublicKey   string // the wallet's key in full, for its copy button only
	Fingerprint string
	HashName    string // this wallet's hash name (spec 7.7)
	Verified    bool   // a state verified: Name, Fee and Customers come from it
	Name        string // the wallet's name, if any
	Fee         string // the network's new-key fee
	Lead        int64  // the signers' time a draft gives by default
	MaxLead     int64  // the most it can give: how far ahead a node takes a request (node.MaxRequestLookahead)
	Customers   []pendingRow
	Step        *requestStep // what the draft or sign step made
	Message     string
	Error       string
}

// requests is the Requests page. Its "draft" step (the default) drafts a request and signs nothing, its
// "sign" step signs pasted request text in the roles this wallet holds, and its "submit" step posts a
// complete request to the node.
func (walletHandler *handler) requests(responseWriter http.ResponseWriter, request *http.Request) {
	wallet := walletHandler.wallet
	step := request.FormValue("step")
	if request.Method == http.MethodPost && step == "submit" {
		if err := wallet.SubmitNameRequest(request.FormValue("request")); err != nil {
			http.Redirect(responseWriter, request, "/requests?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(responseWriter, request, "/requests?msg="+url.QueryEscape("request submitted — it applies in the block it names"), http.StatusSeeOther)
		return
	}
	data := requestsPageData{PublicKey: wallet.PublicKey(), Fingerprint: fingerprint(wallet.PublicKey()), HashName: hashName(wallet.PublicKey()),
		Lead: defaultRequestLead, MaxLead: node.MaxRequestLookahead, Message: request.URL.Query().Get("msg"), Error: request.URL.Query().Get("err")}
	checkpoint, err := wallet.verifiedState()
	if err == nil && request.Method == http.MethodPost {
		var made requestStep
		if step == "sign" {
			made, err = wallet.SignNameRequest(checkpoint, request.FormValue("request"))
		} else {
			made, err = wallet.DraftNameRequest(checkpoint, requestForm{Kind: request.FormValue("kind"), Name: request.FormValue("name"),
				Requestor: request.FormValue("requestor"), NewKey: request.FormValue("newKey"), Bank: request.FormValue("bank"), Lead: request.FormValue("lead")})
		}
		if err != nil {
			http.Redirect(responseWriter, request, "/requests?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		data.Step = &made
	}
	if err != nil {
		data.Error = "cannot read a verified state from the node: " + err.Error()
	} else {
		data.Verified = true
		data.Name = nameOfKey(checkpoint.State.Accounts, wallet.PublicKey())
		data.Fee = ledger.FormatAmount(checkpoint.State.Parameters.NewKeyFee)
		data.Customers = customerChanges(checkpoint.State.Accounts, data.Name)
	}
	walletHandler.execute(responseWriter, "requests.html", data)
}

type adminPageData struct {
	Verified           bool // a state verified: IsValidator comes from it
	IsValidator        bool
	Parameters         node.ParametersView // the parameters in force
	TotalPower         int64
	Quorum             int64 // 0 when the node's governance view could not be read
	ParameterProposals []node.ParameterProposalView
	Reassignments      []node.ReassignmentView
	Draft              *voteDraft // a vote to check before signing
	SignedVote         *voteDraft // a signed vote to hand to the other validators, and what it binds
	Message            string
	Error              string
}

func (walletHandler *handler) admin(responseWriter http.ResponseWriter, request *http.Request) {
	checkpoint, err := walletHandler.wallet.verifiedState()
	walletHandler.renderAdmin(responseWriter, adminPageData{Message: request.URL.Query().Get("msg"), Error: request.URL.Query().Get("err")}, checkpoint, err)
}

// renderAdmin shows the admin page: the node's governance view, around what a vote step put in data.
// Whether this wallet is a validator comes from the verified state the page read.
func (walletHandler *handler) renderAdmin(responseWriter http.ResponseWriter, data adminPageData, checkpoint node.Checkpoint, readErr error) {
	wallet := walletHandler.wallet
	if readErr != nil && data.Error == "" {
		data.Error = "cannot read a verified state from the node: " + readErr.Error()
	}
	name := nameOfKey(checkpoint.State.Accounts, wallet.PublicKey())
	data.Verified = readErr == nil
	data.IsValidator = name != "" && consensus.NewValidatorSet(checkpoint.Validators).Contains(name)
	if view, err := wallet.Governance(); err != nil {
		if data.Error == "" {
			data.Error = "cannot reach node: " + err.Error()
		}
	} else {
		data.Parameters = view.Parameters
		data.TotalPower = view.TotalPower
		data.Quorum = view.Quorum
		data.ParameterProposals = view.ParameterProposals
		data.Reassignments = view.Reassignments
	}
	walletHandler.execute(responseWriter, "admin.html", data)
}

func (walletHandler *handler) proposeParameter(responseWriter http.ResponseWriter, request *http.Request) {
	walletHandler.vote(responseWriter, request, func(checkpoint node.Checkpoint) (voteDraft, error) {
		return walletHandler.wallet.DraftParameters(checkpoint, parameterForm{
			TransactionCost: request.FormValue("cost"), BlockIntervalMs: request.FormValue("blockIntervalMs"),
			StepTimeoutMs: request.FormValue("stepTimeoutMs"), MaxPeers: request.FormValue("maxPeers"),
			MaxMessageBytes: request.FormValue("maxMessageBytes"), NewKeyFee: request.FormValue("newKeyFee"),
			ChangeDelayBlocks: request.FormValue("changeDelayBlocks"), RetainBlocks: request.FormValue("retainBlocks"),
			MaxPaymentAmount: request.FormValue("maxPaymentAmount"), MaxAccountBalance: request.FormValue("maxAccountBalance"),
		})
	}, walletHandler.wallet.SignParameters, walletHandler.wallet.SubmitParameters)
}

func (walletHandler *handler) voteReassignment(responseWriter http.ResponseWriter, request *http.Request) {
	walletHandler.vote(responseWriter, request, func(checkpoint node.Checkpoint) (voteDraft, error) {
		return walletHandler.wallet.DraftReassignment(checkpoint, nameActionForm{Name: request.FormValue("name"), NewAmount: request.FormValue("amount"),
			NewKey: request.FormValue("newKey"), Bank: request.FormValue("bank"), Freeze: request.FormValue("freeze")})
	}, walletHandler.wallet.SignReassignment, walletHandler.wallet.SubmitReassignment)
}

// vote runs the step of a governance vote the form names. "review" (the default) drafts the vote and
// shows exactly what it would sign; "sign" signs a draft, the one just reviewed or one pasted, and shows
// the signed vote to hand to the other validators, sending nothing (unless this key alone is a quorum:
// then it submits); "submit" assembles the pasted votes into one certificate and submits it.
func (walletHandler *handler) vote(responseWriter http.ResponseWriter, request *http.Request, draft func(node.Checkpoint) (voteDraft, error),
	sign func(node.Checkpoint, string) (voteDraft, bool, error), submit func(string) error) {
	if request.Method != http.MethodPost {
		http.Redirect(responseWriter, request, "/admin", http.StatusSeeOther)
		return
	}
	var page adminPageData
	var checkpoint node.Checkpoint
	var err error
	submitted := false
	if step := request.FormValue("step"); step == "submit" {
		err = submit(request.FormValue("votes"))
		submitted = true
	} else if checkpoint, err = walletHandler.wallet.verifiedState(); err == nil && step == "sign" {
		var signed voteDraft
		signed, submitted, err = sign(checkpoint, request.FormValue("draft"))
		page.SignedVote = &signed
	} else if err == nil {
		var drafted voteDraft
		drafted, err = draft(checkpoint)
		page.Draft = &drafted
	}
	switch {
	case err != nil:
		http.Redirect(responseWriter, request, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
	case submitted:
		http.Redirect(responseWriter, request, "/admin?msg="+url.QueryEscape("certificate submitted — it enacts in the next block"), http.StatusSeeOther)
	default:
		walletHandler.renderAdmin(responseWriter, page, checkpoint, nil)
	}
}

func first(text string, count int) string {
	if len(text) <= count {
		return text
	}
	return text[:count]
}
