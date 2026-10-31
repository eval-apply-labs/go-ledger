// Command pocdyn (verifier PoC harness, not production): a REAL multi-process DYNAMIC-validator
// network of the production cmd/server binary. Genesis = the documented single founder F. F pays
// each of four banks; then bank laptops "sleep" (SIGSTOP) and wake (SIGCONT). Only the production
// catchUpLoop -> bestCheckpoint path can bring a woken node forward.
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
	"syscall"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
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
}
type fileConfig struct {
	NetworkID          string          `json:"networkId"`
	Listen             string          `json:"listen"`
	PublicKey          string          `json:"publicKey"`
	PrivateKey         string          `json:"privateKey"`
	TransactionCost    string          `json:"transactionCost"`
	TimeoutMs          int             `json:"timeoutMs"`
	MinBlockIntervalMs int             `json:"minBlockIntervalMs"`
	ValidatorThreshold string          `json:"validatorThreshold"`
	Validators         []fileValidator `json:"validators"`
	Peers              []string        `json:"peers"`
	Genesis            []fileAccount   `json:"genesis"`
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
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
	if json.NewDecoder(resp.Body).Decode(&m) != nil {
		return -1
	}
	h, _ := m["height"].(float64)
	return int64(h)
}

func checkpoint(url string) (node.Checkpoint, error) {
	var cp node.Checkpoint
	resp, err := client.Get(url + "/hdls/checkpoint")
	if err != nil {
		return cp, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&cp)
	return cp, err
}

func main() {
	pay := flag.String("pay", "240", "amount F pays each bank (240 => F keeps 40 < threshold: full rotation; 150 => F keeps 400 and must co-sign every commit)")
	bin := flag.String("bin", "", "server binary")
	dir := flag.String("dir", "", "work dir")
	sleepFor := flag.Duration("sleep", 6*time.Second, "laptop sleep duration")
	noSleep := flag.Duration("nosleep", 0, "if >0: no induced sleeps, just watch natural attrition this long")
	flag.Parse()
	os.MkdirAll(*dir, 0o755)
	const network = "pocdyn"
	b64 := base64.StdEncoding.EncodeToString
	names := []string{"F", "B1", "B2", "B3", "B4"}
	n := len(names)
	pubs, privs, urls, ports := make([][]byte, n), make([][]byte, n), make([]string, n), make([]int, n)
	for i := range names {
		pubs[i], privs[i], _ = signing.Ed25519{}.GenerateKey()
		ports[i] = freePort()
		urls[i] = fmt.Sprintf("http://127.0.0.1:%d", ports[i])
	}
	var vals []fileValidator
	for i := range names {
		vals = append(vals, fileValidator{PublicKey: b64(pubs[i]), Power: 1, URL: urls[i]})
	}
	genesis := []fileAccount{{PublicKey: b64(pubs[0]), Name: "founder", Amount: "1000", ValidatorNode: b64(pubs[0])}}
	procs := make([]*exec.Cmd, n)
	for i := range names {
		var peers []string
		for j := range names {
			if j != i {
				peers = append(peers, urls[j])
			}
		}
		fc := fileConfig{NetworkID: network, Listen: fmt.Sprintf("127.0.0.1:%d", ports[i]), PublicKey: b64(pubs[i]),
			PrivateKey: b64(privs[i]), TransactionCost: "0.1", TimeoutMs: 1000, MinBlockIntervalMs: 300,
			ValidatorThreshold: "100", Validators: vals, Peers: peers, Genesis: genesis}
		data, _ := json.MarshalIndent(fc, "", "  ")
		p := filepath.Join(*dir, names[i]+".json")
		os.WriteFile(p, data, 0o600)
		logf, _ := os.Create(filepath.Join(*dir, names[i]+".log"))
		procs[i] = exec.Command(*bin, "-config", p)
		procs[i].Stdout, procs[i].Stderr = logf, logf
		if err := procs[i].Start(); err != nil {
			panic(err)
		}
	}
	defer func() {
		for _, p := range procs {
			p.Process.Signal(syscall.SIGCONT)
			p.Process.Kill()
			p.Wait()
		}
	}()
	start := time.Now()
	ts := func() string { return fmt.Sprintf("t=%5.1fs", time.Since(start).Seconds()) }
	heights := func() []int64 {
		hs := make([]int64, n)
		for i := range urls {
			hs[i] = status(urls[i])
		}
		return hs
	}
	waitAll := func(min int64, limit time.Duration) {
		dl := time.Now().Add(limit)
		for time.Now().Before(dl) {
			ok := true
			for _, h := range heights() {
				if h < min {
					ok = false
				}
			}
			if ok {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitAll(3, 30*time.Second)
	h := status(urls[0])
	fmt.Printf("%s warm heights=%v (genesis validator set = {F})\n", ts(), heights())
	founder := signing.NewSigner(signing.Ed25519{}, network, pubs[0], privs[0])
	amt, _ := new(big.Rat).SetString(*pay)
	for i := 1; i < n; i++ {
		p, _ := ledger.SignPayment(founder, ledger.Payment{PublicKeyFrom: b64(pubs[0]), PublicKeyTo: b64(pubs[i]), Amount: amt, BlockID: h + 2})
		body, _ := json.Marshal(p)
		resp, err := http.Post(urls[0]+"/hdls/pay", "application/json", bytes.NewReader(body))
		code := -1
		if err == nil {
			code = resp.StatusCode
			resp.Body.Close()
		}
		fmt.Printf("%s F pays %s %s at block %d -> HTTP %d\n", ts(), names[i], *pay, h+2, code)
	}
	waitAll(h+6, 30*time.Second)
	cp, err := checkpoint(urls[1])
	if err == nil {
		label := map[string]string{}
		for i, k := range pubs {
			label[b64(k)] = names[i]
		}
		var vs, signers []string
		for _, v := range cp.Validators {
			vs = append(vs, fmt.Sprintf("%s:%d", label[v.PublicKey], v.Power))
		}
		for _, v := range cp.Commit.Voters() {
			signers = append(signers, label[v])
		}
		// Genesis-anchor test exactly as bestCheckpoint applies it (validators derived from genesis = {F:1000}).
		genesisSet := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: b64(pubs[0]), Power: 1000}})
		var vouched int64
		for _, v := range cp.Commit.Voters() {
			vouched += genesisSet.Power(v)
		}
		fmt.Printf("%s after payout: heights=%v signingSet=%v lastCommitSigners=%v genesisVouched=%d/1000 (needs >333)\n", ts(), heights(), vs, signers, vouched)
	}
	time.Sleep(2 * time.Second)
	sleepWake := func(i int) {
		fmt.Printf("%s %s laptop sleeps (SIGSTOP) heights=%v\n", ts(), names[i], heights())
		procs[i].Process.Signal(syscall.SIGSTOP)
		time.Sleep(*sleepFor)
		procs[i].Process.Signal(syscall.SIGCONT)
		fmt.Printf("%s %s laptop wakes (SIGCONT) heights=%v\n", ts(), names[i], heights())
	}
	watch := func(d time.Duration) {
		dl := time.Now().Add(d)
		for time.Now().Before(dl) {
			time.Sleep(3 * time.Second)
			fmt.Printf("%s heights[F,B1,B2,B3,B4]=%v\n", ts(), heights())
		}
	}
	if *noSleep > 0 {
		watch(*noSleep)
		return
	}
	sleepWake(4)
	watch(21 * time.Second)
	sleepWake(3)
	watch(24 * time.Second)
}
