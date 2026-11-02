package main

import (
	"bytes"
	"net"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A customer's wallet works against a bank's real public face (spec §8, §10): cmd/fetcher, built here,
// serving a real engine's public folder and passing submissions on to the engine's operator port. A
// payment is sent and its receipt verifies, an account update publishes a public address and two IP
// addresses, the admin page shows the governance the fetcher serves, and a freeze lands. The fetcher is
// a main package, so the test builds it with the go command; it skips under -short, and when no
// toolchain is at hand to build it.
func TestCustomersWalletWorksAgainstTheRealFetcher(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped under -short: it builds cmd/fetcher and runs it over a real engine")
	}
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("skipped: no go command to build cmd/fetcher with (%v)", err)
	}
	fetcherBinary := filepath.Join(t.TempDir(), "fetcher")
	if output, err := exec.Command(goCommand, "build", "-o", fetcherBinary, "github.com/eval-apply-labs/go-ledger/cmd/fetcher").CombinedOutput(); err != nil {
		if strings.Contains(string(output), "toolchain") {
			t.Skipf("skipped: no toolchain to build cmd/fetcher with: %s", output)
		}
		t.Fatalf("building cmd/fetcher: %v: %s", err, output)
	}

	wallet := newTestWallet(t, "")
	bankEngine := startEngine(t, wallet)
	operatorPort := httptest.NewServer(bankEngine.handler)
	t.Cleanup(operatorPort.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0") // a free port for the fetcher, released just before it starts
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	recordFolder := t.TempDir()
	fetcher := exec.Command(fetcherBinary, "-listen", address, "-folder", bankEngine.publicFolder, "-record", recordFolder,
		"-lifelong", filepath.Join(recordFolder, "lifelong.jsonl"), "-engine", operatorPort.URL)
	var fetcherLog bytes.Buffer
	fetcher.Stdout, fetcher.Stderr = &fetcherLog, &fetcherLog
	if err := fetcher.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = fetcher.Process.Kill()
		_ = fetcher.Wait()
		if t.Failed() {
			t.Logf("the fetcher's log:\n%s", fetcherLog.String())
		}
	})
	wallet.nodeURL = "http://" + address
	eventually(t, "verified state through the fetcher", func() bool { _, err := wallet.verifiedState(); return err == nil })

	if message := redirectMessage(submitForm(wallet, "/send", url.Values{"step": {"confirm"}, "to": {"shop"}, "amount": {"10"}})); !strings.Contains(message, "receipt from validator bank-a verified") {
		t.Errorf("the payment: %q; want a receipt from bank-a that verifies", message)
	}
	update := url.Values{"description": {"a customer"}, "publicAddress": {"https://alice.example"}, "ipAddresses": {"203.0.113.7 2001:db8::7"}}
	if recorder := submitForm(wallet, "/update", update); redirectMessage(recorder) == "" {
		t.Errorf("the account update was refused: %q", redirectError(recorder))
	}
	eventually(t, "commit of the payment and the update", func() bool {
		alice := accountIn(wallet, "alice")
		return hasAmount(alice, "90") && alice.Description == "a customer" && alice.PublicAddress == "https://alice.example" &&
			slices.Equal(alice.IPAddresses, []string{"203.0.113.7", "2001:db8::7"})
	})
	if body := page(t, wallet, "/admin"); !strings.Contains(body, "cost 0.1") {
		t.Error("the admin page does not show the governance the fetcher serves")
	}
	if message := redirectMessage(submitForm(wallet, "/freeze", url.Values{"step": {"confirm"}})); !strings.Contains(message, "freeze of alice sent") {
		t.Errorf("the freeze: %q", message)
	}
	eventually(t, "commit of the freeze", func() bool { return accountIn(wallet, "alice").Frozen })
}
