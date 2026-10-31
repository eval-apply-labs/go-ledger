// Command pocmisc (verifier PoC harness, not production): real multi-process networks of the
// production cmd/server binary for NEW-7 (non-validator observer with maxPeers>0) and NB-1
// (a static validator's on-chain Address vanishing when its account is emptied).
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type fileValidator struct {
	PublicKey string `json:"publicKey"`
	Power     int64  `json:"power"`
	URL       string `json:"url,omitempty"`
}
type fileAccount struct {
	PublicKey     string `json:"publicKey"`
	Amount        string `json:"amount"`
	ValidatorNode string `json:"validatorNode"`
	Address       string `json:"address,omitempty"`
}
type fileConfig struct {
	NetworkID          string          `json:"networkId"`
	Listen             string          `json:"listen"`
	PublicKey          string          `json:"publicKey"`
	PrivateKey         string          `json:"privateKey"`
	TransactionCost    string          `json:"transactionCost"`
	TimeoutMs          int             `json:"timeoutMs"`
	MinBlockIntervalMs int             `json:"minBlockIntervalMs"`
	MaxPeers           int             `json:"maxPeers"`
	Validators         []fileValidator `json:"validators"`
	Peers              []string        `json:"peers"`
	Bootstrap          []string        `json:"bootstrap,omitempty"`
	Genesis            []fileAccount   `json:"genesis"`
}

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

var client = &http.Client{Timeout: 2 * time.Second}

func status(url string) int64 {
	resp, err := client.Get(url + "/hdls/status")
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	h, _ := m["height"].(float64)
	return int64(h)
}

func accounts(url string) []ledger.Account {
	resp, err := client.Get(url + "/hdls/accounts")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var a []ledger.Account
	json.NewDecoder(resp.Body).Decode(&a)
	return a
}

func main() {
	mode := flag.String("mode", "observer", "observer | drain")
	maxPeers := flag.Int("maxpeers", 2, "maxPeers")
	bin := flag.String("bin", "", "server binary")
	dir := flag.String("dir", "", "work dir")
	flag.Parse()
	os.MkdirAll(*dir, 0o755)
	const network = "pocmisc"
	b64 := base64.StdEncoding.EncodeToString
	nv := 4
	total := nv
	if *mode == "observer" {
		total = nv + 1 // index 4 = non-validator observer
	}
	pubs, privs, urls, ports := make([][]byte, total), make([][]byte, total), make([]string, total), make([]int, total)
	for i := 0; i < total; i++ {
		pubs[i], privs[i], _ = signing.Ed25519{}.GenerateKey()
		ports[i] = freePort()
		urls[i] = fmt.Sprintf("http://127.0.0.1:%d", ports[i])
	}
	var vals []fileValidator
	var genesis []fileAccount
	for i := 0; i < nv; i++ {
		vals = append(vals, fileValidator{PublicKey: b64(pubs[i]), Power: 1, URL: urls[i]})
		a := fileAccount{PublicKey: b64(pubs[i]), Amount: "100", ValidatorNode: b64(pubs[i])}
		if *mode == "drain" {
			a.Address = urls[i] // every validator publishes its address in genesis (full peer DNS)
		}
		genesis = append(genesis, a)
	}
	var procs []*exec.Cmd
	for i := 0; i < total; i++ {
		var peers []string
		for j := 0; j < nv; j++ {
			if j != i {
				peers = append(peers, urls[j])
			}
		}
		fc := fileConfig{NetworkID: network, Listen: fmt.Sprintf("127.0.0.1:%d", ports[i]), PublicKey: b64(pubs[i]),
			PrivateKey: b64(privs[i]), TransactionCost: "0.1", TimeoutMs: 1000, MinBlockIntervalMs: 300,
			MaxPeers: *maxPeers, Validators: vals, Peers: peers, Genesis: genesis}
		if i >= nv {
			fc.Bootstrap = append([]string(nil), urls[:nv]...) // observer follows by certified checkpoints
		}
		data, _ := json.MarshalIndent(fc, "", "  ")
		p := filepath.Join(*dir, fmt.Sprintf("node%d.json", i))
		os.WriteFile(p, data, 0o600)
		logf, _ := os.Create(filepath.Join(*dir, fmt.Sprintf("node%d.log", i)))
		c := exec.Command(*bin, "-config", p)
		c.Stdout, c.Stderr = logf, logf
		c.Start()
		procs = append(procs, c)
	}
	defer func() {
		for _, p := range procs {
			p.Process.Kill()
			p.Wait()
		}
	}()
	start := time.Now()
	ts := func() string { return fmt.Sprintf("t=%5.1fs", time.Since(start).Seconds()) }
	heights := func() []int64 {
		hs := make([]int64, total)
		for i := range urls {
			hs[i] = status(urls[i])
		}
		return hs
	}
	for time.Since(start) < 30*time.Second {
		ok := true
		for _, h := range heights()[:nv] {
			if h < 3 {
				ok = false
			}
		}
		if ok {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(4 * time.Second) // let the observer's catch-up poll land
	fmt.Printf("%s warm heights=%v\n", ts(), heights())
	pay := func(from, to int, amount string, via int, lookahead int64) {
		h := status(urls[0]) // the NETWORK tip (a lagging observer would otherwise pick a stale block)
		amt, _ := new(big.Rat).SetString(amount)
		s := signing.NewSigner(signing.Ed25519{}, network, pubs[from], privs[from])
		p, _ := ledger.SignPayment(s, ledger.Payment{PublicKeyFrom: b64(pubs[from]), PublicKeyTo: b64(pubs[to]), Amount: amt, BlockID: h + lookahead})
		body, _ := json.Marshal(p)
		resp, err := http.Post(urls[via]+"/hdls/pay", "application/json", bytes.NewReader(body))
		if err != nil {
			fmt.Printf("%s pay err %v\n", ts(), err)
			return
		}
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		resp.Body.Close()
		fmt.Printf("%s node%d pays node%d %s via node%d for block %d -> HTTP %d %s\n", ts(), from, to, amount, via, h+lookahead, resp.StatusCode, strings.TrimSpace(buf.String())[:min(90, len(strings.TrimSpace(buf.String())))])
	}
	bal := func(url string, who int) string {
		for _, a := range accounts(url) {
			if a.PublicKey == b64(pubs[who]) {
				return a.Amount.RatString() + fmt.Sprintf("(addr=%v)", a.Address != "")
			}
		}
		return "none"
	}
	switch *mode {
	case "observer":
		// Control: the same payment shape submitted to a validator lands.
		pay(0, 1, "10", 4, 8) // via the observer (non-validator, maxPeers>0)
		time.Sleep(12 * time.Second)
		fmt.Printf("%s heights=%v node1 balance on validator0=%s, on observer=%s (expected 109.9 if the observer's payment landed)\n", ts(), heights(), bal(urls[0], 1), bal(urls[4], 1))
		pay(2, 3, "10", 2, 3) // control via a validator
		time.Sleep(8 * time.Second)
		fmt.Printf("%s heights=%v node3 balance on validator0=%s (control: expected 109.9)\n", ts(), heights(), bal(urls[0], 3))
	case "drain":
		for _, i := range []int{1, 2} {
			pay(i, 0, "100", i, 3) // validator i moves its whole balance out: account (and Address) deleted
			for k := 0; k < 6; k++ {
				time.Sleep(3 * time.Second)
				fmt.Printf("%s heights=%v node%d account on node0=%s\n", ts(), heights(), i, bal(urls[0], i))
			}
		}
		for k := 0; k < 4; k++ {
			time.Sleep(3 * time.Second)
			fmt.Printf("%s heights=%v\n", ts(), heights())
		}
	}
}
