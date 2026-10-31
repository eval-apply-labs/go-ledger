package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

func main() {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	c := base64.StdEncoding.EncodeToString(pub)
	nl := c[:10] + "\n" + c[10:20] + "\r\n" + c[20:]
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.StdEncoding.Strict()} {
		raw, err := enc.DecodeString(nl)
		fmt.Printf("newline-split spelling decodes to same key: %v (err=%v)\n", err == nil && string(raw) == string(pub), err)
	}
}
