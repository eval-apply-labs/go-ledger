package node

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func vbsAliases(t *testing.T, canonical string) []string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	b := []byte(canonical)
	idx := bytes.IndexByte([]byte(alphabet), b[42])
	var out []string
	for bits := 1; bits < 4; bits++ {
		c := append([]byte(nil), b...)
		c[42] = alphabet[(idx&^3)|bits]
		out = append(out, string(c))
	}
	out = append(out, canonical[:20]+"\n"+canonical[20:], canonical+"\r\n")
	raw, _ := base64.StdEncoding.DecodeString(canonical)
	for _, a := range out {
		r1, e1 := base64.StdEncoding.DecodeString(a)
		r2, e2 := base64.StdEncoding.Strict().DecodeString(a)
		if e1 != nil || !bytes.Equal(r1, raw) {
			t.Fatalf("alias %q not accepted by StdEncoding", a)
		}
		t.Logf("alias %q: StdEncoding ok; Strict ok=%v same-key=%v", a[38:], e2 == nil, bytes.Equal(r2, raw))
	}
	return out
}

// C18 through the production intake: a delegation to an alias spelling arrives over HTTP
// (/hdls/update), commits, and the alias becomes a second validator identity for one key, whose
// votes verify and are counted separately from the canonical spelling's.
func TestVBSC18AliasViaHTTPIntake(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	h, _ := signing.Generate(signing.Ed25519{}, "testnet")
	d, _ := signing.Generate(signing.Ed25519{}, "testnet")
	V, Hk, D := v.PublicKeyBase64(), h.PublicKeyBase64(), d.PublicKeyBase64()
	aliases := vbsAliases(t, V)
	alias := aliases[0]
	threshold := rat("50")
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: h, ValidatorThreshold: threshold,
		TransactionCost: rat("0.1"), Timeout: 400 * time.Millisecond, MinBlockInterval: 40 * time.Millisecond,
		Genesis: []ledger.Account{
			{PublicKey: Hk, Amount: rat("1000"), ValidatorNode: Hk},
			{PublicKey: V, Amount: rat("100"), ValidatorNode: V},
			{PublicKey: D, Amount: rat("100"), ValidatorNode: Hk},
		}}, NewHTTPTransport(nil))
	srv := httptest.NewServer(NewServer(nd).Handler())
	defer srv.Close()
	nd.Start()
	defer nd.Stop()
	waitFor(t, 3*time.Second, func() bool { return nd.Snapshot().Height >= 2 }, "no progress")
	bid := nd.Snapshot().Height + 2
	upd, _ := ledger.SignAccountUpdate(d, ledger.AccountUpdate{From: D, Delegated: alias, BlockID: bid})
	body, _ := json.Marshal(upd)
	resp, err := http.Post(srv.URL+"/hdls/update", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	t.Logf("POST /hdls/update delegating to an alias spelling → HTTP %d", resp.StatusCode)
	waitFor(t, 5*time.Second, func() bool {
		return deriveValidators(nd.Snapshot().Accounts, threshold).Contains(alias)
	}, "alias delegation did not commit")
	set := deriveValidators(nd.Snapshot().Accounts, threshold)
	t.Logf("committed set: canonical V power=%d, alias V' power=%d (ONE key, two identities)", set.Power(V), set.Power(alias))
	var x, y [32]byte
	x[0], y[0] = 1, 2
	vx, _ := consensus.SignVote(v, consensus.Vote{Height: 50, Round: 0, Type: consensus.Precommit, BlockDigest: x, Validator: V})
	vy, _ := consensus.SignVote(v, consensus.Vote{Height: 50, Round: 0, Type: consensus.Precommit, BlockDigest: y, Validator: alias})
	vsPre := consensus.NewVoteSet(set, 50, 0, consensus.Precommit)
	c1, _ := vsPre.Add(signing.Ed25519{}, "testnet", vx)
	c2, _ := vsPre.Add(signing.Ed25519{}, "testnet", vy)
	t.Logf("same key precommits X as V and Y as V': both counted=%v/%v, evidence=%d", c1, c2, len(vsPre.Evidence()))
	if c1 && c2 && len(vsPre.Evidence()) == 0 {
		t.Errorf("one key = two validator identities via base64 aliasing; cross-spelling double-sign is uncounted-as-equivocation and unslashable")
	}
}
