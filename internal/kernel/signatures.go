package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

const MaxProviderSignatureProofs = 256

// SignatureProof lets a session attest an opaque provider signature to its
// exact issuer, normalized conversation prefix and target tool call without
// persisting the signature itself.
type SignatureProof struct {
	Digest        string         `json:"digest"`
	IssuerRef     extensions.Ref `json:"issuerRef"`
	ContextDigest string         `json:"contextDigest"`
	TargetDigest  string         `json:"targetDigest"`
}

// NewSignatureProof creates a non-secret proof bound to the provider issuer,
// normalized prefix digest and exact tool-call target.
func NewSignatureProof(value string, issuer extensions.Ref, contextDigest string, call normalize.ToolCall) (SignatureProof, error) {
	if value == "" || issuer.Kind != "provider-definition" || issuer.Validate() != nil {
		return SignatureProof{}, errors.New("signature proof requires a value and exact provider-definition issuer")
	}
	if !validSignatureDigest(contextDigest) {
		return SignatureProof{}, errors.New("signature proof requires a valid conversation-context digest")
	}
	targetDigest, err := SignatureTargetDigest(call)
	if err != nil {
		return SignatureProof{}, err
	}
	digest := sha256.Sum256([]byte(value))
	return SignatureProof{Digest: hex.EncodeToString(digest[:]), IssuerRef: issuer, ContextDigest: contextDigest, TargetDigest: targetDigest}, nil
}

func validateSignatureProof(proof SignatureProof) error {
	digest, err := hex.DecodeString(proof.Digest)
	if err != nil || len(digest) != sha256.Size || proof.IssuerRef.Kind != "provider-definition" || proof.IssuerRef.Validate() != nil || !validSignatureDigest(proof.ContextDigest) || !validSignatureDigest(proof.TargetDigest) {
		return errors.New("invalid provider signature proof")
	}
	return nil
}

// SignatureTargetDigest prevents a proof for one signed tool call from being
// moved onto another call in the same otherwise-identical conversation turn.
func SignatureTargetDigest(call normalize.ToolCall) (string, error) {
	if call.ID == "" || call.Name == "" {
		return "", errors.New("signature proof target requires a tool-call ID and name")
	}
	arguments, err := canonicalSignatureArguments(call.Arguments)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{ID: call.ID, Name: call.Name, Arguments: arguments})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalSignatureArguments(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	var encoded []byte
	switch typed := value.(type) {
	case json.RawMessage:
		encoded = append([]byte(nil), typed...)
	case string:
		encoded = []byte(typed)
	default:
		var err error
		encoded, err = json.Marshal(value)
		if err != nil {
			return nil, err
		}
	}
	var canonical any
	if err := json.Unmarshal(encoded, &canonical); err != nil {
		return nil, errors.New("signature proof tool arguments are not valid JSON")
	}
	result, err := json.Marshal(canonical)
	return json.RawMessage(result), err
}

func validSignatureDigest(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == sha256.Size
}

// SignatureContextDigest binds provider signatures to the exact normalized
// prefix before a message, plus the system prompt and tool contract. Opaque
// signatures are removed from earlier messages before hashing to avoid making
// proof verification recursively depend on the signature being verified.
func SignatureContextDigest(request normalize.Request, prefixMessages int) (string, error) {
	if prefixMessages < 0 || prefixMessages > len(request.Messages) {
		return "", errors.New("signature context message boundary is out of range")
	}
	digests, err := signatureContextDigests(request)
	if err != nil {
		return "", err
	}
	return digests[prefixMessages], nil
}

func signatureContextDigests(request normalize.Request) ([]string, error) {
	context := struct {
		Prompt     normalize.PromptPlan `json:"prompt,omitempty"`
		Tools      []normalize.Tool     `json:"tools,omitempty"`
		ToolChoice normalize.ToolChoice `json:"toolChoice,omitempty"`
	}{Prompt: request.Prompt, Tools: request.Tools, ToolChoice: request.ToolChoice}
	encoded, err := json.Marshal(context)
	if err != nil {
		return nil, err
	}
	hasher := sha256.New()
	_, _ = hasher.Write(encoded)
	_, _ = hasher.Write([]byte{0})
	digests := make([]string, len(request.Messages)+1)
	digests[0] = hex.EncodeToString(hasher.Sum(nil))
	for messageIndex, source := range request.Messages {
		message := source
		message.ToolCalls = append([]normalize.ToolCall(nil), source.ToolCalls...)
		for callIndex := range message.ToolCalls {
			message.ToolCalls[callIndex].Signature = ""
			message.ToolCalls[callIndex].SignatureIssuer = extensions.Ref{}
		}
		encodedMessage, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		_, _ = hasher.Write(encodedMessage)
		_, _ = hasher.Write([]byte{0})
		digests[messageIndex+1] = hex.EncodeToString(hasher.Sum(nil))
	}
	return digests, nil
}

func attestToolCallSignatures(request *normalize.Request, proofs []SignatureProof, issuer extensions.Ref) {
	if request == nil {
		return
	}
	needsContext := false
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			if call.Signature != "" {
				needsContext = true
				break
			}
		}
		if needsContext {
			break
		}
	}
	var contextDigests []string
	var contextErr error
	if needsContext {
		contextDigests, contextErr = signatureContextDigests(*request)
	}
	for messageIndex := range request.Messages {
		for callIndex := range request.Messages[messageIndex].ToolCalls {
			call := &request.Messages[messageIndex].ToolCalls[callIndex]
			if call.Signature == "" {
				continue
			}
			contextDigest := ""
			if contextErr == nil && messageIndex < len(contextDigests) {
				contextDigest = contextDigests[messageIndex]
			}
			proof, err := NewSignatureProof(call.Signature, issuer, contextDigest, *call)
			if contextErr != nil || err != nil {
				request.UnsupportedFacets = append(request.UnsupportedFacets, FacetReasoningSignature)
				continue
			}
			trusted := false
			for _, saved := range proofs {
				if validateSignatureProof(saved) == nil && saved.Digest == proof.Digest && saved.IssuerRef == issuer && saved.ContextDigest == contextDigest && saved.TargetDigest == proof.TargetDigest {
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

func appendSignatureProof(proofs []SignatureProof, signature string, issuer extensions.Ref, contextDigest string, call normalize.ToolCall) ([]SignatureProof, error) {
	proof, err := NewSignatureProof(signature, issuer, contextDigest, call)
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
