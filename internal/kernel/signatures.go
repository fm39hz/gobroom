package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

const MaxProviderSignatureProofs = 256

// SignatureProof lets a session attest that an opaque signature value was
// emitted by one exact provider definition without persisting the signature.
type SignatureProof struct {
	Digest    string         `json:"digest"`
	IssuerRef extensions.Ref `json:"issuerRef"`
}

func NewSignatureProof(value string, issuer extensions.Ref) (SignatureProof, error) {
	if value == "" || issuer.Kind != "provider-definition" || issuer.Validate() != nil {
		return SignatureProof{}, errors.New("signature proof requires a value and exact provider-definition issuer")
	}
	digest := sha256.Sum256([]byte(value))
	return SignatureProof{Digest: hex.EncodeToString(digest[:]), IssuerRef: issuer}, nil
}

func validateSignatureProof(proof SignatureProof) error {
	digest, err := hex.DecodeString(proof.Digest)
	if err != nil || len(digest) != sha256.Size || proof.IssuerRef.Kind != "provider-definition" || proof.IssuerRef.Validate() != nil {
		return errors.New("invalid provider signature proof")
	}
	return nil
}

func attestToolCallSignatures(request *normalize.Request, proofs []SignatureProof, issuer extensions.Ref) {
	if request == nil {
		return
	}
	for messageIndex := range request.Messages {
		for callIndex := range request.Messages[messageIndex].ToolCalls {
			call := &request.Messages[messageIndex].ToolCalls[callIndex]
			if call.Signature == "" {
				continue
			}
			proof, err := NewSignatureProof(call.Signature, issuer)
			if err != nil {
				request.UnsupportedFacets = append(request.UnsupportedFacets, FacetReasoningSignature)
				continue
			}
			trusted := false
			for _, saved := range proofs {
				if validateSignatureProof(saved) == nil && saved.Digest == proof.Digest && saved.IssuerRef == issuer {
					trusted = true
					break
				}
			}
			if trusted {
				call.SignatureIssuer = issuer
			} else {
				call.SignatureIssuer = extensions.Ref{}
				request.UnsupportedFacets = append(request.UnsupportedFacets, FacetReasoningSignature)
			}
		}
	}
	request.UnsupportedFacets = uniqueSorted(request.UnsupportedFacets)
}

func appendSignatureProof(proofs []SignatureProof, signature string, issuer extensions.Ref) ([]SignatureProof, error) {
	proof, err := NewSignatureProof(signature, issuer)
	if err != nil {
		return proofs, err
	}
	for _, existing := range proofs {
		if existing == proof {
			return proofs, nil
		}
	}
	if len(proofs) >= MaxProviderSignatureProofs {
		return proofs, ErrSignatureProofLimit
	}
	return append(proofs, proof), nil
}
