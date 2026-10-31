// Command pocnet (verifier PoC harness, not production) runs a REAL multi-process network of the
// production cmd/server binary on localhost, optionally publishing some validators' on-chain
// gossip addresses (via genesis or via a signed AccountUpdate POSTed to /hdls/update), and
// records each node's height over time.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	Name          string `json:"name,omitempty"`
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
	SnapshotPath       string          `json:"snapshotPath,omitempty"`
	Genesis            []fileAccount   `json:"genesis"`
}

func parseSet(s string) map[int]bool {
	m := map[int]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		i, err := strconv.Atoi(f)
		if err != nil {
			panic(err)
		}
		m[i] = true
	}
	return m
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func status(url string) (int64, error) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url + "/hdls/status")
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return -1, err
	}
	h, _ := m["height"].(float64)
	return int64(h), nil
}

func addresses(url string) (int, error) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url + "/hdls/accounts")
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()
	var accts []ledger.Account
	if err := json.NewDecoder(resp.Body).Decode(&accts); err != nil {
		return -1, err
	}
	n := 0
	for _, a := range accts {
		if a.Address != "" {
			n++
		}
	}
	return n, nil
}

func main() {
	n := flag.Int("n", 4, "validators")
	maxPeers := flag.Int("maxpeers", 0, "maxPeers")
	publish := flag.String("publish", "", "comma list of validator indices publishing via AccountUpdate")
	genesisAddr := flag.String("genesisaddr", "", "comma list of validator indices with an address in genesis")
	dur := flag.Duration("dur", 40*time.Second, "observe duration after publication")
	timeoutMs := flag.Int("timeout", 1000, "timeoutMs")
	intervalMs := flag.Int("interval", 300, "minBlockIntervalMs")
	warm := flag.Int64("warm", 3, "height to reach before publishing")
	dir := flag.String("dir", "", "work dir")
	bin := flag.String("bin", "", "server binary")
	powers := flag.String("powers", "", "comma list of powers (default 1 each)")
	publish2 := flag.String("publish2", "", "second batch of publishers, 12s after the first")
	flag.Parse()

	pubSet, genSet := parseSet(*publish), parseSet(*genesisAddr)
	pw := make([]int64, *n)
	for i := range pw {
		pw[i] = 1
	}
	if *powers != "" {
		for i, f := range strings.Split(*powers, ",") {
			v, _ := strconv.ParseInt(f, 10, 64)
			pw[i] = v
		}
	}
	os.MkdirAll(*dir, 0o755)
	network := "pocnet"
	pubs := make([][]byte, *n)
	privs := make([][]byte, *n)
	urls := make([]string, *n)
	ports := make([]int, *n)
	for i := 0; i < *n; i++ {
		pub, priv, err := signing.Ed25519{}.GenerateKey()
		if err != nil {
			panic(err)
		}
		pubs[i], privs[i] = pub, priv
		ports[i] = freePort()
		urls[i] = fmt.Sprintf("http://127.0.0.1:%d", ports[i])
	}
	b64 := base64.StdEncoding.EncodeToString
	var vals []fileValidator
	var genesis []fileAccount
	for i := 0; i < *n; i++ {
		vals = append(vals, fileValidator{PublicKey: b64(pubs[i]), Power: pw[i], URL: urls[i]})
		acct := fileAccount{PublicKey: b64(pubs[i]), Amount: "100", ValidatorNode: b64(pubs[i])}
		if genSet[i] {
			acct.Address = urls[i]
		}
		genesis = append(genesis, acct)
	}
	var procs []*exec.Cmd
	for i := 0; i < *n; i++ {
		var peers []string
		for j := 0; j < *n; j++ {
			if j != i {
				peers = append(peers, urls[j])
			}
		}
		fc := fileConfig{
			NetworkID: network, Listen: fmt.Sprintf("127.0.0.1:%d", ports[i]),
			PublicKey: b64(pubs[i]), PrivateKey: b64(privs[i]), TransactionCost: "0.1",
			TimeoutMs: *timeoutMs, MinBlockIntervalMs: *intervalMs, MaxPeers: *maxPeers,
			Validators: vals, Peers: peers, Genesis: genesis,
		}
		data, _ := json.MarshalIndent(fc, "", "  ")
		cfgPath := filepath.Join(*dir, fmt.Sprintf("node%d.json", i))
		os.WriteFile(cfgPath, data, 0o600)
		logf, _ := os.Create(filepath.Join(*dir, fmt.Sprintf("node%d.log", i)))
		cmd := exec.Command(*bin, "-config", cfgPath)
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			panic(err)
		}
		procs = append(procs, cmd)
	}
	defer func() {
		for _, p := range procs {
			p.Process.Kill()
			p.Wait()
		}
	}()
	heights := func() []int64 {
		hs := make([]int64, *n)
		for i := range urls {
			hs[i], _ = status(urls[i])
		}
		return hs
	}
	start := time.Now()
	// warm up
	for {
		hs := heights()
		ok := true
		for _, h := range hs {
			if h < *warm {
				ok = false
			}
		}
		if ok {
			fmt.Printf("t=%5.1fs warm heights=%v\n", time.Since(start).Seconds(), hs)
			break
		}
		if time.Since(start) > 60*time.Second {
			fmt.Printf("t=%5.1fs WARMUP FAILED heights=%v\n", time.Since(start).Seconds(), hs)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	publishBatch := func(pubSet map[int]bool) {
		h, _ := status(urls[0])
		target := h + 2
		for i := range pubSet {
			signer := signing.NewSigner(signing.Ed25519{}, network, pubs[i], privs[i])
			u, err := ledger.SignAccountUpdate(signer, ledger.AccountUpdate{From: b64(pubs[i]), Address: urls[i], BlockID: target})
			if err != nil {
				panic(err)
			}
			body, _ := json.Marshal(u)
			resp, err := http.Post(urls[i]+"/hdls/update", "application/json", bytes.NewReader(body))
			code := -1
			if err == nil {
				code = resp.StatusCode
				resp.Body.Close()
			}
			fmt.Printf("t=%5.1fs node%d POST /hdls/update address=%s blockID=%d -> %d %v\n", time.Since(start).Seconds(), i, urls[i], target, code, err)
		}
	}
	if len(pubSet) > 0 {
		publishBatch(pubSet)
	}
	if p2 := parseSet(*publish2); len(p2) > 0 {
		go func() { time.Sleep(12 * time.Second); publishBatch(p2) }()
	}
	pubStart := time.Now()
	for time.Since(pubStart) < *dur {
		time.Sleep(2 * time.Second)
		hs := heights()
		var as []int
		for i := range urls {
			a, _ := addresses(urls[i])
			as = append(as, a)
		}
		fmt.Printf("t=%5.1fs heights=%v addressesInState=%v\n", time.Since(start).Seconds(), hs, as)
	}
}
