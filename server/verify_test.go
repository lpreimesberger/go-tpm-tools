package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"fmt"
	"io"
	"testing"

	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm-tools/internal"
	"github.com/google/go-tpm-tools/internal/test"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpmutil"
)

func getDigestHash(input string) []byte {
	inputDigestHash := sha256.New()
	inputDigestHash.Write([]byte(input))
	return inputDigestHash.Sum(nil)
}

func extendPCRsRandomly(rwc io.ReadWriteCloser, selpcr tpm2.PCRSelection) error {
	var pcrExtendValue []byte
	if selpcr.Hash == tpm2.AlgSHA256 {
		pcrExtendValue = make([]byte, 32)
	} else if selpcr.Hash == tpm2.AlgSHA1 {
		pcrExtendValue = make([]byte, 20)
	}

	for _, v := range selpcr.PCRs {
		_, err := rand.Read(pcrExtendValue)
		if err != nil {
			return fmt.Errorf("random bytes read fail %v", err)
		}
		err = tpm2.PCRExtend(rwc, tpmutil.Handle(v), selpcr.Hash, pcrExtendValue, "")
		if err != nil {
			return fmt.Errorf("PCR extend fail %v", err)
		}
	}
	return nil
}

func TestVerifyHappyCases(t *testing.T) {
	rwc := test.GetTPM(t)
	defer client.CheckedClose(t, rwc)

	onePCR := []int{test.DebugPCR}
	twoPCR := append(onePCR, test.ApplicationPCR)
	dupePCR := append(twoPCR, twoPCR...)

	subtests := []struct {
		name         string
		getKey       func(io.ReadWriter) (*client.Key, error)
		pcrHashAlgo  tpm2.Algorithm
		quotePCRList []int
		extraData    []byte
	}{
		{"AK-RSA_SHA1_2PCRs_nonce", client.AttestationKeyRSA, tpm2.AlgSHA1, twoPCR, getDigestHash("test")},
		{"AK-RSA_SHA1_1PCR_nonce", client.AttestationKeyRSA, tpm2.AlgSHA1, onePCR, getDigestHash("t")},
		{"AK-RSA_SHA1_1PCR_no-nonce", client.AttestationKeyRSA, tpm2.AlgSHA1, onePCR, nil},
		{"AK-RSA_SHA256_2PCRs_nonce", client.AttestationKeyRSA, tpm2.AlgSHA256, twoPCR, getDigestHash("test")},
		{"AK-RSA_SHA256_2PCR_empty-nonce", client.AttestationKeyRSA, tpm2.AlgSHA256, twoPCR, []byte{}},
		{"AK-RSA_SHA256_dupePCrSel_nonce", client.AttestationKeyRSA, tpm2.AlgSHA256, dupePCR, getDigestHash("")},

		{"AK-ECC_SHA1_2PCRs_nonce", client.AttestationKeyECC, tpm2.AlgSHA1, twoPCR, getDigestHash("test")},
		{"AK-ECC_SHA1_1PCR_nonce", client.AttestationKeyECC, tpm2.AlgSHA1, onePCR, getDigestHash("t")},
		{"AK-ECC_SHA1_1PCR_no-nonce", client.AttestationKeyECC, tpm2.AlgSHA1, onePCR, nil},
		{"AK-ECC_SHA256_2PCRs_nonce", client.AttestationKeyECC, tpm2.AlgSHA256, twoPCR, getDigestHash("test")},
		{"AK-ECC_SHA256_2PCR_empty-nonce", client.AttestationKeyECC, tpm2.AlgSHA256, twoPCR, []byte{}},
		{"AK-ECC_SHA256_dupePCrSel_nonce", client.AttestationKeyECC, tpm2.AlgSHA256, dupePCR, getDigestHash("")},
	}
	for _, subtest := range subtests {
		t.Run(subtest.name, func(t *testing.T) {
			ak, err := subtest.getKey(rwc)
			if err != nil {
				t.Errorf("failed to generate AK: %v", err)
			}
			defer ak.Close()

			selpcr := tpm2.PCRSelection{
				Hash: subtest.pcrHashAlgo,
				PCRs: subtest.quotePCRList,
			}
			err = extendPCRsRandomly(rwc, selpcr)
			if err != nil {
				t.Fatalf("failed to extend test PCRs: %v", err)
			}
			quote, err := ak.Quote(selpcr, subtest.extraData)
			if err != nil {
				t.Fatalf("failed to quote: %v", err)
			}
			err = internal.VerifyQuote(quote, ak.PublicKey(), subtest.extraData)
			if err != nil {
				t.Fatalf("failed to verify: %v", err)
			}
		})
	}
}

func TestVerifyPCRChanged(t *testing.T) {
	rwc := test.GetTPM(t)
	defer client.CheckedClose(t, rwc)

	ak, err := client.AttestationKeyRSA(rwc)
	if err != nil {
		t.Errorf("failed to generate AK: %v", err)
	}
	defer ak.Close()

	selpcr := tpm2.PCRSelection{
		Hash: tpm2.AlgSHA256,
		PCRs: []int{test.DebugPCR},
	}
	err = extendPCRsRandomly(rwc, selpcr)
	if err != nil {
		t.Errorf("failed to extend test PCRs: %v", err)
	}
	nonce := getDigestHash("test")
	quote, err := ak.Quote(selpcr, nonce)
	if err != nil {
		t.Error(err)
	}

	// change the PCR value
	err = extendPCRsRandomly(rwc, selpcr)
	if err != nil {
		t.Errorf("failed to extend test PCRs: %v", err)
	}

	quote.Pcrs, err = client.ReadPCRs(rwc, selpcr)
	if err != nil {
		t.Errorf("failed to read PCRs: %v", err)
	}
	err = internal.VerifyQuote(quote, ak.PublicKey(), nonce)
	if err == nil {
		t.Errorf("Verify should fail as Verify read a modified PCR")
	}
}

func TestVerifyUsingDifferentPCR(t *testing.T) {
	rwc := test.GetTPM(t)
	defer client.CheckedClose(t, rwc)

	ak, err := client.AttestationKeyRSA(rwc)
	if err != nil {
		t.Errorf("failed to generate AK: %v", err)
	}
	defer ak.Close()

	err = extendPCRsRandomly(rwc, tpm2.PCRSelection{
		Hash: tpm2.AlgSHA256,
		PCRs: []int{test.DebugPCR, test.ApplicationPCR},
	})
	if err != nil {
		t.Errorf("failed to extend test PCRs: %v", err)
	}

	nonce := getDigestHash("test")
	quote, err := ak.Quote(tpm2.PCRSelection{
		Hash: tpm2.AlgSHA256,
		PCRs: []int{test.DebugPCR},
	}, nonce)
	if err != nil {
		t.Error(err)
	}

	quote.Pcrs, err = client.ReadPCRs(rwc, tpm2.PCRSelection{
		Hash: tpm2.AlgSHA256,
		PCRs: []int{test.ApplicationPCR},
	})
	if err != nil {
		t.Errorf("failed to read PCRs: %v", err)
	}
	err = internal.VerifyQuote(quote, ak.PublicKey(), nonce)
	if err == nil {
		t.Errorf("Verify should fail as Verify read a different PCR")
	}
}

func TestVerifyBasicAttestation(t *testing.T) {
	rwc := test.GetTPM(t)
	defer client.CheckedClose(t, rwc)

	ak, err := client.AttestationKeyRSA(rwc)
	if err != nil {
		t.Fatalf("failed to generate AK: %v", err)
	}
	defer ak.Close()

	nonce := []byte("super secret nonce")
	attestation, err := ak.Attest(client.AttestOpts{Nonce: nonce})
	if err != nil {
		t.Fatalf("failed to attest: %v", err)
	}

	if _, err := VerifyAttestation(attestation, VerifyOpts{
		Nonce:      nonce,
		TrustedAKs: []crypto.PublicKey{ak.PublicKey()},
	}); err != nil {
		t.Errorf("failed to verify: %v", err)
	}

	if _, err := VerifyAttestation(attestation, VerifyOpts{
		Nonce:      append(nonce, 0),
		TrustedAKs: []crypto.PublicKey{ak.PublicKey()},
	}); err == nil {
		t.Error("using the wrong nonce should make verification fail")
	}

	if _, err := VerifyAttestation(attestation, VerifyOpts{
		Nonce: nonce,
	}); err == nil {
		t.Error("using no trusted AKs should make verification fail")
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAttestation(attestation, VerifyOpts{
		Nonce:      nonce,
		TrustedAKs: []crypto.PublicKey{priv.Public()},
	}); err == nil {
		t.Error("using a random trusted AKs should make verification fail")
	}
}

func TestVerifySHA1Attestation(t *testing.T) {
	rwc := test.GetTPM(t)
	defer client.CheckedClose(t, rwc)

	ak, err := client.AttestationKeyRSA(rwc)
	if err != nil {
		t.Fatalf("failed to generate AK: %v", err)
	}
	defer ak.Close()

	nonce := []byte("super secret nonce")
	attestation, err := ak.Attest(client.AttestOpts{Nonce: nonce})
	if err != nil {
		t.Fatalf("failed to attest: %v", err)
	}

	// We should get a SHA-256 state, even if we allow SHA-1
	opts := VerifyOpts{
		Nonce:      nonce,
		TrustedAKs: []crypto.PublicKey{ak.PublicKey()},
		AllowSHA1:  true,
	}
	state, err := VerifyAttestation(attestation, opts)
	if err != nil {
		t.Errorf("failed to verify: %v", err)
	}
	h := tpm2.Algorithm(state.GetHash())
	if h != tpm2.AlgSHA256 {
		t.Errorf("expected SHA-256 state, got: %v", h)
	}

	// Now we mess up the SHA-256 state to force SHA-1 fallback
	for _, quote := range attestation.GetQuotes() {
		if tpm2.Algorithm(quote.GetPcrs().GetHash()) == tpm2.AlgSHA256 {
			quote.Quote = nil
		}
	}
	state, err = VerifyAttestation(attestation, opts)
	if err != nil {
		t.Errorf("failed to verify: %v", err)
	}
	h = tpm2.Algorithm(state.GetHash())
	if h != tpm2.AlgSHA1 {
		t.Errorf("expected SHA-1 state, got: %v", h)
	}

	// SHA-1 fallback can then be disabled
	opts.AllowSHA1 = false
	if _, err = VerifyAttestation(attestation, opts); err == nil {
		t.Error("expected attestation to fail with only SHA-1")
	}
}
