/*
Copyright 2018 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package attestation defines methods to attest a message using Pgp Private and
// Public Key pair.
package attestation

import (
	"bytes"
	"crypto"
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/grafeas/kritis/pkg/kritis/secrets"
	"github.com/pkg/errors"

	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"
	"golang.org/x/crypto/openpgp/packet"
	"golang.org/x/crypto/openpgp/s2k"
)

const (
	RSABits = 4096
)

var pgpConfig = packet.Config{
	// Use Sha256
	DefaultHash:            crypto.SHA256,
	DefaultCipher:          packet.CipherAES256,
	DefaultCompressionAlgo: packet.CompressionZLIB,
	CompressionConfig: &packet.CompressionConfig{
		Level: packet.DefaultCompression,
	},
	RSABits: RSABits,
}

// VerifyMessageAttestation verifies if the image is attested using the PEM
// encoded public key.
func VerifyMessageAttestation(pubKey string, sig string, message string) error {
	text, err := GetPlainMessage(pubKey, sig)
	if err != nil {
		return err
	}
	// Finally, make sure the signature is over the right message.
	if string(text) != message {
		return fmt.Errorf("signature could not be verified. got: %s, want: %s", text, message)
	}
	return nil
}

// GetPlainMessage verifies if the image is attested using the PEM
// encoded public key and returns the plain text in bytes
func GetPlainMessage(pubKey string, sig string) ([]byte, error) {
	keyring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pubKey))
	if err != nil {
		return nil, errors.Wrap(err, "read armored key ring")
	}
	buf := bytes.NewBufferString(sig)
	armorBlock, err := armor.Decode(buf)
	if err != nil {
		return nil, errors.Wrap(err, "could not decode armor signature")
	}
	md, err := openpgp.ReadMessage(armorBlock.Body, keyring, nil, &pgpConfig)
	if err != nil {
		return nil, errors.Wrap(err, "could not read armor signature")
	}

	// MessageDetails.UnverifiedBody signature is not verified until we read it.
	// This will call PublicKey.VerifySignature for the keys in the keyring.
	plaintext, err := ioutil.ReadAll(md.UnverifiedBody)
	if err != nil {
		return nil, errors.Wrap(err, "could not verify armor signature")
	}
	// Make sure after reading the UnverifiedBody above, there is no signature error.
	if md.SignatureError != nil {
		return nil, fmt.Errorf("bad signature found: %s", md.SignatureError)
	}
	if md.Signature == nil {
		return nil, fmt.Errorf("no signature found for given key")
	}

	return plaintext, nil
}

// CreateMessageAttestation attests the message using the given PGP key.
// pgpKey: PGP key
// message: Message to attest
func CreateMessageAttestation(pgpKey *secrets.PgpKey, message string) (string, error) {
	// First Create a signer Entity from public and private keys.
	signer, err := createEntityFromKeys(pgpKey.PublicKey(), pgpKey.PrivateKey())
	if err != nil {
		return "", errors.Wrap(err, "creating entity keys")
	}

	b := new(bytes.Buffer)
	// Armor Encode it.
	armorWriter, errEncode := armor.Encode(b, openpgp.SignatureType, make(map[string]string))
	if errEncode != nil {
		return "", errors.Wrap(err, "encoding data")
	}
	// Finally Sign the Text.
	w, err := openpgp.Sign(armorWriter, signer, nil, &pgpConfig)
	if err != nil {
		return "", errors.Wrap(err, "opengpg signing")
	}

	_, err = w.Write([]byte(message))
	if err != nil {
		return "", errors.Wrap(err, "writing signed data")
	}
	w.Close()
	armorWriter.Close()
	return string(b.Bytes()), nil
}

func createEntityFromKeys(pubKey *packet.PublicKey, privKey *packet.PrivateKey) (*openpgp.Entity, error) {
	currentTime := pgpConfig.Now()
	uid := packet.NewUserId("", "", "")
	if uid == nil {
		return nil, errors.New("user id field contained invalid characters")
	}

	e := &openpgp.Entity{
		PrimaryKey: pubKey,
		PrivateKey: privKey,
		Identities: make(map[string]*openpgp.Identity),
	}
	isPrimaryID := true
	e.Identities[uid.Id] = &openpgp.Identity{
		Name:   uid.Id,
		UserId: uid,
		SelfSignature: &packet.Signature{
			CreationTime: currentTime,
			SigType:      packet.SigTypePositiveCert,
			PubKeyAlgo:   packet.PubKeyAlgoRSA,
			Hash:         pgpConfig.Hash(),
			IsPrimaryId:  &isPrimaryID,
			FlagsValid:   true,
			FlagSign:     true,
			FlagCertify:  true,
			IssuerKeyId:  &e.PrimaryKey.KeyId,
		},
	}
	err := e.Identities[uid.Id].SelfSignature.SignUserId(uid.Id, e.PrimaryKey, e.PrivateKey, &pgpConfig)
	if err != nil {
		return nil, err
	}

	// Set Config Hash from Config
	hashID, ok := s2k.HashToHashId(pgpConfig.DefaultHash)
	if !ok {
		return nil, fmt.Errorf("tried to convert unknown hash %d", pgpConfig.DefaultHash)
	}
	e.Identities[uid.Id].SelfSignature.PreferredHash = []uint8{hashID}

	// Set Config Cipher from Config
	e.Identities[uid.Id].SelfSignature.PreferredSymmetric = []uint8{uint8(pgpConfig.DefaultCipher)}

	return e, nil
}
